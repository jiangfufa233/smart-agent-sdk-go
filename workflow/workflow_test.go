package workflow

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jiangfufa233/smart-agent-sdk-go/v2/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/session"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/testutil"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func task(id, text string) Task {
	return Task{
		ID: id,
		Agent: &agent.Agent{
			ID:    id + "-agent",
			Name:  id,
			Model: testutil.NewScripted(testutil.TextStep(text)),
		},
	}
}

func TestSequenceForwardsTaskEvents(t *testing.T) {
	run := NewRunner(nil).Run(context.Background(), Sequence(task("research", "facts"), task("write", "answer")))
	var starts []string
	var forwarded Event
	for event := range run.Events {
		if event.Type == agent.StreamTaskStarted {
			starts = append(starts, event.TaskID)
		}
		if event.Type == agent.StreamTextDelta {
			forwarded = event
		}
	}
	res, err := run.Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(starts) != 2 || starts[0] != "research" || starts[1] != "write" {
		t.Fatalf("task starts = %v", starts)
	}
	if res.RootRunID == "" || res.Tasks["research"].Result.Output != "facts" || res.Tasks["write"].Result.Output != "answer" {
		t.Fatalf("result = %+v", res)
	}
	if forwarded.StreamEvent == nil || forwarded.RootRunID != res.RootRunID || forwarded.TaskID == "" || forwarded.StreamEvent.Task.SourceRunID == "" || forwarded.StreamEvent.Task.RootRunID != res.RootRunID {
		t.Fatalf("forwarded event = %+v", forwarded)
	}
}

func TestSequenceDoesNotMutateCallerTasks(t *testing.T) {
	tasks := []Task{task("first", "one"), task("second", "two"), task("third", "three")}
	tasks[2].DependsOn = []string{"extra"}
	before := make([]Task, len(tasks))
	copy(before, tasks)

	plan := Sequence(tasks...)
	if len(plan.Tasks[1].DependsOn) != 1 || plan.Tasks[1].DependsOn[0] != "first" {
		t.Fatalf("plan dependencies = %v", plan.Tasks[1].DependsOn)
	}
	if len(plan.Tasks[2].DependsOn) != 2 || plan.Tasks[2].DependsOn[0] != "extra" || plan.Tasks[2].DependsOn[1] != "second" {
		t.Fatalf("plan dependencies = %v", plan.Tasks[2].DependsOn)
	}
	for i := range tasks {
		if len(tasks[i].DependsOn) != len(before[i].DependsOn) {
			t.Fatalf("caller task %d DependsOn changed: %v -> %v", i, before[i].DependsOn, tasks[i].DependsOn)
		}
	}
	for i, id := range tasks[2].DependsOn {
		if id != before[2].DependsOn[i] {
			t.Fatalf("caller task 2 DependsOn changed: %v -> %v", before[2].DependsOn, tasks[2].DependsOn)
		}
	}
}

func TestParallelRespectsLimit(t *testing.T) {
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var calls atomic.Int32
	blockingTask := func(id string) Task {
		return Task{ID: id, Agent: &agent.Agent{
			ID: id + "-agent", Name: id,
			Model: model.ModelFunc(func(ctx context.Context, _ *model.Request) (*model.Response, error) {
				calls.Add(1)
				started <- struct{}{}
				select {
				case <-release:
					return &model.Response{Message: model.Message{Role: model.RoleAssistant, Content: id}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}),
		}}
	}
	run := NewRunner(nil).Run(context.Background(), Parallel(2, blockingTask("a"), blockingTask("b"), blockingTask("c")))
	<-started
	<-started
	select {
	case <-started:
		t.Fatal("started more tasks than MaxParallel")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if _, err := run.Wait(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestContinueSkipsFailedDependencies(t *testing.T) {
	boom := errors.New("boom")
	failed := Task{ID: "failed", Agent: &agent.Agent{
		ID: "failed-agent", Name: "failed",
		Model: model.ModelFunc(func(context.Context, *model.Request) (*model.Response, error) {
			return nil, boom
		}),
	}}
	independent := task("independent", "done")
	dependentCalls := atomic.Int32{}
	dependent := Task{ID: "dependent", DependsOn: []string{"failed"}, Agent: &agent.Agent{
		ID: "dependent-agent", Name: "dependent",
		Model: model.ModelFunc(func(context.Context, *model.Request) (*model.Response, error) {
			dependentCalls.Add(1)
			return nil, errors.New("must not run")
		}),
	}}
	run := NewRunner(nil).Run(context.Background(), Plan{Tasks: []Task{failed, independent, dependent}, MaxParallel: 2, FailureMode: Continue})
	var skipped bool
	for event := range run.Events {
		if event.Type == agent.StreamTaskSkipped && event.TaskID == "dependent" {
			skipped = true
		}
	}
	res, err := run.Result()
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if !skipped || !res.Tasks["dependent"].Skipped || res.Tasks["independent"].Result == nil || dependentCalls.Load() != 0 {
		t.Fatalf("result = %+v, calls=%d", res, dependentCalls.Load())
	}
}

func TestRejectsInvalidPlan(t *testing.T) {
	a := task("a", "a")
	b := task("b", "b")
	a.DependsOn = []string{"b"}
	b.DependsOn = []string{"a"}
	run := NewRunner(nil).Run(context.Background(), Plan{Tasks: []Task{a, b}})
	var terminal Event
	for event := range run.Events {
		terminal = event
	}
	_, err := run.Result()
	if err == nil || terminal.Type != agent.StreamRunError {
		t.Fatalf("terminal=%+v err=%v", terminal, err)
	}
}

func TestFailFastCancelsActiveTasks(t *testing.T) {
	boom := errors.New("boom")
	started := make(chan struct{})
	blocked := Task{ID: "blocked", Agent: &agent.Agent{
		ID: "blocked-agent", Name: "blocked",
		Model: model.ModelFunc(func(ctx context.Context, _ *model.Request) (*model.Response, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	}}
	failing := Task{ID: "failing", Agent: &agent.Agent{
		ID: "failing-agent", Name: "failing",
		Model: model.ModelFunc(func(context.Context, *model.Request) (*model.Response, error) {
			<-started
			return nil, boom
		}),
	}}
	never := task("never", "must not run")
	run := NewRunner(nil).Run(context.Background(), Plan{Tasks: []Task{blocked, failing, never}, MaxParallel: 2, FailureMode: FailFast})
	var skipped bool
	for event := range run.Events {
		if event.Type == agent.StreamTaskSkipped && event.TaskID == "never" {
			skipped = true
		}
	}
	res, err := run.Result()
	if !errors.Is(err, boom) || !skipped || !res.Tasks["never"].Skipped || !errors.Is(res.Tasks["blocked"].Err, boom) {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}

func TestCancelDrainsActiveTask(t *testing.T) {
	cause := errors.New("operator stopped workflow")
	started := make(chan struct{})
	blocked := Task{ID: "blocked", Agent: &agent.Agent{
		ID: "blocked-agent", Name: "blocked",
		Model: model.ModelFunc(func(ctx context.Context, _ *model.Request) (*model.Response, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	}}
	run := NewRunner(nil).Run(context.Background(), Plan{Tasks: []Task{blocked}})
	for event := range run.Events {
		if event.Type == agent.StreamTaskStarted {
			<-started
			run.Cancel(cause)
		}
	}
	res, err := run.Result()
	if !errors.Is(err, cause) || !errors.Is(res.Tasks["blocked"].Err, cause) {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}

func TestRejectsParallelSharedSession(t *testing.T) {
	shared := session.NewInMemory()
	left := task("left", "left")
	left.Session = shared
	right := task("right", "right")
	right.Session = shared
	run := NewRunner(nil).Run(context.Background(), Parallel(2, left, right))
	for range run.Events {
	}
	_, err := run.Result()
	if err == nil || !strings.Contains(err.Error(), "same session") {
		t.Fatalf("err=%v", err)
	}
}
