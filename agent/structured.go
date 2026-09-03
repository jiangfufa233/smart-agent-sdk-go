package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/tool"
)

// StructuredOutputError reports that the final output of a run could not be
// decoded into the requested type. Raw carries the unparsed model text.
type StructuredOutputError struct {
	Raw string
	Err error
}

func (e *StructuredOutputError) Error() string {
	return fmt.Sprintf("agent: structured output: %v", e.Err)
}

func (e *StructuredOutputError) Unwrap() error { return e.Err }

// TypedResult pairs the decoded structured output with the underlying run.
type TypedResult[T any] struct {
	// Value is the model's final output decoded into T.
	Value T
	// Result is the underlying run result.
	Result *RunResult
}

// RunTyped runs a like Runner.Run but constrains the model to produce JSON
// matching the schema of T and decodes the final output into a T.
//
// When the agent has no ResponseFormat configured, a json_schema response
// format derived from T is injected on a copy of the agent; the original is
// left untouched. A Markdown code fence around the output is tolerated.
func RunTyped[T any](ctx context.Context, r *Runner, a *Agent, input string) (*TypedResult[T], error) {
	ta, err := typedAgent[T](a)
	if err != nil {
		return nil, err
	}
	res, err := r.Run(ctx, ta, input)
	if err != nil {
		return nil, err
	}
	value, err := decodeTyped[T](res.Output)
	if err != nil {
		return nil, err
	}
	return &TypedResult[T]{Value: value, Result: res}, nil
}

// typedAgent returns a shallow copy of a with a json_schema response format
// derived from T, unless one is already configured.
func typedAgent[T any](a *Agent) (*Agent, error) {
	if a.Settings != nil && a.Settings.ResponseFormat != nil {
		return a, nil
	}
	schema, err := tool.SchemaFromType(reflect.TypeFor[T]())
	if err != nil {
		return nil, fmt.Errorf("agent: structured output: schema for %s: %w", reflect.TypeFor[T](), err)
	}
	cp := *a
	settings := settingsFor(a)
	settings.ResponseFormat = &model.ResponseFormat{
		Type: model.FormatJSONSchema,
		JSONSchema: &model.JSONSchema{
			Name:   schemaName(reflect.TypeFor[T]()),
			Schema: schema,
			// The reflected schema is not strict-mode compliant (no
			// additionalProperties:false, optional keys not in required),
			// so providers are not asked to enforce it.
			Strict: false,
		},
	}
	cp.Settings = &settings
	return &cp, nil
}

func decodeTyped[T any](raw string) (T, error) {
	var value T
	if err := json.Unmarshal([]byte(stripCodeFence(raw)), &value); err != nil {
		return value, &StructuredOutputError{Raw: raw, Err: err}
	}
	return value, nil
}

// stripCodeFence removes a surrounding Markdown code fence that models add
// despite a JSON response format.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	nl := strings.IndexByte(s, '\n')
	if nl < 0 {
		return s
	}
	body := s[nl+1:]
	if end := strings.LastIndex(body, "```"); end >= 0 {
		body = body[:end]
	}
	return strings.TrimSpace(body)
}

// schemaName derives the json_schema format name from T.
func schemaName(t reflect.Type) string {
	name := t.Name()
	if i := strings.IndexByte(name, '['); i >= 0 {
		name = name[:i]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "response"
	}
	s := b.String()
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}
