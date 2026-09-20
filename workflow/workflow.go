// Package workflow executes explicitly declared multi-agent task graphs.
package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/jiangfufa233/smart-agent-sdk-go/agent"
)

// FailureMode controls how the workflow reacts when a task fails.
type FailureMode string

const (
	// Continue runs independent tasks after a failure and skips only dependents.
	Continue FailureMode = "continue"
	// FailFast cancels active tasks and skips every task not yet started.
	FailFast FailureMode = "fail_fast"
)

// Task is one independently executable agent task in a workflow graph.
type Task struct {
	ID        string
	ParentID  string
	Agent     *agent.Agent
	Input     string
	DependsOn []string
	// BuildInput, when set, produces the task input from the results of the
	// task's dependencies at admission time. It runs synchronously in the
	// scheduling loop, so it should honor ctx cancellation and return
	// promptly; an error fails the task.
	BuildInput func(context.Context, Results) (string, error)
	Session    agent.Session
}

// Plan declares a DAG of tasks. Tasks without dependencies may run concurrently.
type Plan struct {
	Tasks       []Task
	MaxParallel int
	FailureMode FailureMode
}

// Sequence returns a plan that runs tasks in the supplied order. The tasks
// and their DependsOn slices are copied; the caller's data is not modified.
func Sequence(tasks ...Task) Plan {
	copied := make([]Task, len(tasks))
	copy(copied, tasks)
	for i := 1; i < len(copied); i++ {
		deps := make([]string, 0, len(copied[i].DependsOn)+1)
		deps = append(deps, copied[i].DependsOn...)
		copied[i].DependsOn = append(deps, copied[i-1].ID)
	}
	return Plan{Tasks: copied, MaxParallel: 1, FailureMode: Continue}
}

// Parallel returns a plan that permits up to max concurrent independent tasks.
func Parallel(max int, tasks ...Task) Plan {
	return Plan{Tasks: tasks, MaxParallel: max, FailureMode: Continue}
}

// Runner executes workflow plans with an agent Runner. A nil AgentRunner uses
// agent.NewRunner for every task.
type Runner struct {
	AgentRunner *agent.Runner
}

// NewRunner creates a workflow runner using ar. A nil ar uses agent.NewRunner.
func NewRunner(ar *agent.Runner) *Runner { return &Runner{AgentRunner: ar} }

// Event is a normalized workflow update. StreamEvent is populated for forwarded
// operational events emitted by the task's agent run.
type Event struct {
	Type         agent.StreamEventType
	RootRunID    string
	TaskID       string
	ParentTaskID string
	Agent        agent.AgentRef
	StreamEvent  *agent.StreamEvent
	Err          error
}

// TaskResult records the terminal state of one task.
type TaskResult struct {
	Result  *agent.RunResult
	Err     error
	Skipped bool
}

// Results is an immutable snapshot supplied to Task.BuildInput.
type Results map[string]TaskResult

// Result is the terminal workflow state.
type Result struct {
	RootRunID string
	Tasks     Results
}

// Run is an in-progress workflow. Consume Events until it closes, or call
// Wait to drain it before retrieving the result.
type Run struct {
	Events <-chan Event

	events chan Event
	done   chan struct{}
	cancel context.CancelCauseFunc

	mu  sync.RWMutex
	res *Result
	err error
}

// Run starts scheduling p asynchronously.
func (r *Runner) Run(ctx context.Context, p Plan) *Run {
	events := make(chan Event, 256)
	done := make(chan struct{})
	taskCtx, cancel := context.WithCancelCause(ctx)
	run := &Run{Events: events, events: events, done: done, cancel: cancel}
	go func() {
		defer close(done)
		defer close(events)
		res, err := r.execute(ctx, taskCtx, p, run)
		run.mu.Lock()
		run.res, run.err = res, err
		run.mu.Unlock()
	}()
	return run
}

// Cancel prevents further task admission and cancels active tasks.
func (r *Run) Cancel(cause error) {
	if cause == nil {
		cause = context.Canceled
	}
	r.cancel(cause)
}

// Result waits for the workflow to finish and returns its aggregate state.
func (r *Run) Result() (*Result, error) {
	<-r.done
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.res, r.err
}

// Wait drains Events before returning the aggregate state.
func (r *Run) Wait() (*Result, error) {
	for range r.Events {
	}
	return r.Result()
}

type taskState uint8

const (
	pending taskState = iota
	running
	succeeded
	failed
	skipped
)

type taskDone struct {
	id  string
	res *agent.RunResult
	err error
}

func (r *Runner) execute(emitCtx, taskCtx context.Context, p Plan, run *Run) (*Result, error) {
	rootID := workflowID()
	if err := validatePlan(p); err != nil {
		run.emit(emitCtx, Event{Type: agent.StreamRunError, RootRunID: rootID, Err: err})
		return &Result{RootRunID: rootID, Tasks: Results{}}, err
	}
	if p.MaxParallel <= 0 {
		p.MaxParallel = 1
	}
	if p.FailureMode == "" {
		p.FailureMode = Continue
	}

	byID := make(map[string]Task, len(p.Tasks))
	state := make(map[string]taskState, len(p.Tasks))
	for _, task := range p.Tasks {
		byID[task.ID] = task
		state[task.ID] = pending
	}
	results := make(Results, len(p.Tasks))
	activeSessions := map[string]bool{}
	completed, active := 0, 0
	done := make(chan taskDone, len(p.Tasks))
	var firstErr error
	failFast := false

	for completed < len(p.Tasks) {
		if taskCtx.Err() != nil {
			failFast = true
			if firstErr == nil {
				firstErr = context.Cause(taskCtx)
			}
		}
		if failFast {
			for _, task := range p.Tasks {
				if state[task.ID] != pending {
					continue
				}
				state[task.ID] = skipped
				results[task.ID] = TaskResult{Skipped: true, Err: firstErr}
				completed++
				run.emit(emitCtx, taskEvent(agent.StreamTaskSkipped, rootID, task, nil, firstErr))
			}
		} else {
			for _, task := range p.Tasks {
				if state[task.ID] != pending || !hasFailedDependency(task, state) {
					continue
				}
				state[task.ID] = skipped
				results[task.ID] = TaskResult{Skipped: true}
				completed++
				run.emit(emitCtx, taskEvent(agent.StreamTaskSkipped, rootID, task, nil, nil))
			}
			for _, task := range p.Tasks {
				if active >= p.MaxParallel || state[task.ID] != pending || !dependenciesSucceeded(task, state) {
					continue
				}
				key := sessionKey(task.Session)
				if key != "" && activeSessions[key] {
					continue
				}
				input := task.Input
				if task.BuildInput != nil {
					var err error
					input, err = task.BuildInput(taskCtx, dependencySnapshot(task, results))
					if err != nil {
						state[task.ID] = failed
						results[task.ID] = TaskResult{Err: err}
						completed++
						if firstErr == nil {
							firstErr = err
						}
						run.emit(emitCtx, taskEvent(agent.StreamTaskFailed, rootID, task, nil, err))
						if p.FailureMode == FailFast {
							failFast = true
							run.cancel(err)
						}
						continue
					}
				}
				state[task.ID] = running
				active++
				if key != "" {
					activeSessions[key] = true
				}
				run.emit(emitCtx, taskEvent(agent.StreamTaskStarted, rootID, task, nil, nil))
				go r.runTask(emitCtx, taskCtx, rootID, task, input, run, done)
			}
		}

		if active == 0 {
			if completed == len(p.Tasks) {
				break
			}
			if firstErr == nil {
				firstErr = errors.New("workflow: no runnable tasks")
			}
			failFast = true
			continue
		}

		out := <-done
		task := byID[out.id]
		active--
		if key := sessionKey(task.Session); key != "" {
			delete(activeSessions, key)
		}
		completed++
		if out.err != nil {
			taskErr := out.err
			if taskCtx.Err() != nil && context.Cause(taskCtx) != nil {
				taskErr = context.Cause(taskCtx)
			}
			state[out.id] = failed
			results[out.id] = TaskResult{Result: out.res, Err: taskErr}
			if firstErr == nil {
				firstErr = taskErr
			}
			run.emit(emitCtx, taskEvent(agent.StreamTaskFailed, rootID, task, nil, taskErr))
			if p.FailureMode == FailFast {
				failFast = true
				run.cancel(taskErr)
			}
			continue
		}
		state[out.id] = succeeded
		results[out.id] = TaskResult{Result: out.res}
		run.emit(emitCtx, taskEvent(agent.StreamTaskCompleted, rootID, task, nil, nil))
	}

	res := &Result{RootRunID: rootID, Tasks: snapshot(results)}
	if firstErr != nil {
		run.emit(emitCtx, Event{Type: agent.StreamRunError, RootRunID: rootID, Err: firstErr})
		return res, firstErr
	}
	run.emit(emitCtx, Event{Type: agent.StreamFinalOutput, RootRunID: rootID})
	return res, nil
}

func (r *Runner) runTask(emitCtx, ctx context.Context, rootID string, task Task, input string, run *Run, done chan<- taskDone) {
	ref := agent.TaskRef{ID: task.ID, ParentID: task.ParentID, RootRunID: rootID}
	ctx = agent.WithTask(ctx, ref)
	ar := r.AgentRunner
	if ar == nil {
		ar = agent.NewRunner()
	}
	var stream *agent.StreamRun
	if task.Session != nil {
		stream = ar.RunStreamWithSession(ctx, task.Agent, task.Session, input)
	} else {
		stream = ar.RunStream(ctx, task.Agent, input)
	}
	for event := range stream.Events {
		switch event.Type {
		case agent.StreamRunStarted, agent.StreamFinalOutput, agent.StreamRunError:
			continue
		}
		eventCopy := event
		run.emit(emitCtx, taskEvent(event.Type, rootID, task, &eventCopy, nil))
	}
	res, err := stream.Result()
	done <- taskDone{id: task.ID, res: res, err: err}
}

func (r *Run) emit(ctx context.Context, event Event) bool {
	select {
	case r.events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func taskEvent(typ agent.StreamEventType, rootID string, task Task, event *agent.StreamEvent, err error) Event {
	e := Event{
		Type:         typ,
		RootRunID:    rootID,
		TaskID:       task.ID,
		ParentTaskID: task.ParentID,
		Agent:        agentRef(task.Agent),
		StreamEvent:  event,
		Err:          err,
	}
	if event != nil {
		e.Agent = event.Agent
		e.TaskID = event.Task.ID
		e.ParentTaskID = event.Task.ParentID
		e.RootRunID = event.Task.RootRunID
	}
	return e
}

func agentRef(a *agent.Agent) agent.AgentRef {
	if a == nil {
		return agent.AgentRef{}
	}
	return agent.AgentRef{ID: a.ID, Name: a.Name}
}

func dependenciesSucceeded(task Task, state map[string]taskState) bool {
	for _, id := range task.DependsOn {
		if state[id] != succeeded {
			return false
		}
	}
	return true
}

func hasFailedDependency(task Task, state map[string]taskState) bool {
	for _, id := range task.DependsOn {
		if state[id] == failed || state[id] == skipped {
			return true
		}
	}
	return false
}

func snapshot(in Results) Results {
	out := make(Results, len(in))
	for id, result := range in {
		out[id] = result
	}
	return out
}

func dependencySnapshot(task Task, results Results) Results {
	out := make(Results, len(task.DependsOn))
	for _, id := range task.DependsOn {
		out[id] = results[id]
	}
	return out
}

func validatePlan(p Plan) error {
	if len(p.Tasks) == 0 {
		return errors.New("workflow: plan has no tasks")
	}
	if p.MaxParallel < 0 {
		return errors.New("workflow: MaxParallel must not be negative")
	}
	if p.FailureMode != "" && p.FailureMode != Continue && p.FailureMode != FailFast {
		return fmt.Errorf("workflow: unknown failure mode %q", p.FailureMode)
	}
	byID := make(map[string]Task, len(p.Tasks))
	for _, task := range p.Tasks {
		if task.ID == "" {
			return errors.New("workflow: task ID is required")
		}
		if _, exists := byID[task.ID]; exists {
			return fmt.Errorf("workflow: duplicate task ID %q", task.ID)
		}
		if task.Agent == nil {
			return fmt.Errorf("workflow: task %q has nil agent", task.ID)
		}
		byID[task.ID] = task
	}
	for _, task := range p.Tasks {
		seen := make(map[string]struct{}, len(task.DependsOn))
		for _, dep := range task.DependsOn {
			if dep == task.ID {
				return fmt.Errorf("workflow: task %q depends on itself", task.ID)
			}
			if _, ok := byID[dep]; !ok {
				return fmt.Errorf("workflow: task %q depends on unknown task %q", task.ID, dep)
			}
			if _, duplicate := seen[dep]; duplicate {
				return fmt.Errorf("workflow: task %q repeats dependency %q", task.ID, dep)
			}
			seen[dep] = struct{}{}
		}
	}
	if err := validateCycles(byID); err != nil {
		return err
	}
	return validateSessions(p.Tasks, byID)
}

func validateCycles(byID map[string]Task) error {
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("workflow: dependency cycle at task %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dep := range byID[id].DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for id := range byID {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func validateSessions(tasks []Task, byID map[string]Task) error {
	for i, left := range tasks {
		key := sessionKey(left.Session)
		if key == "" {
			continue
		}
		for _, right := range tasks[i+1:] {
			if key != sessionKey(right.Session) {
				continue
			}
			if dependsOn(byID, left.ID, right.ID) || dependsOn(byID, right.ID, left.ID) {
				continue
			}
			return fmt.Errorf("workflow: tasks %q and %q may run concurrently with the same session", left.ID, right.ID)
		}
	}
	return nil
}

func dependsOn(byID map[string]Task, id, target string) bool {
	for _, dep := range byID[id].DependsOn {
		if dep == target || dependsOn(byID, dep, target) {
			return true
		}
	}
	return false
}

func sessionKey(s agent.Session) string {
	if s == nil {
		return ""
	}
	v := reflect.ValueOf(s)
	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Map || v.Kind() == reflect.Func || v.Kind() == reflect.Slice || v.Kind() == reflect.Chan {
		return fmt.Sprintf("%T:%x", s, v.Pointer())
	}
	return fmt.Sprintf("%T:%v", s, s)
}

func workflowID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("workflow-%d", time.Now().UnixNano())
}
