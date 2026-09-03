// Package tool defines the Tool interface and a reflection-based function
// tool adapter with automatic JSON Schema generation.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/example/agent-sdk/model"
)

// Tool is anything an agent can invoke during a run.
type Tool interface {
	// Spec returns the tool definition sent to the model.
	Spec() model.ToolParam
	// Run executes the tool with the raw JSON arguments produced by the model
	// and returns a textual result that is fed back into the conversation.
	Run(ctx context.Context, argumentsJSON string) (string, error)
}

var (
	ctxType = reflect.TypeOf((*context.Context)(nil)).Elem()
	errType = reflect.TypeOf((*error)(nil)).Elem()
)

// FunctionTool adapts a Go function to the Tool interface.
//
// Supported signatures:
//
//	func(in Args) (string, error)
//	func(in Args) (T, error)        // T is JSON marshaled
//	func(ctx context.Context, in Args) (string, error)
//
// Args must be a struct; its JSON Schema is derived via reflection.
type FunctionTool struct {
	name          string
	description   string
	schema        json.RawMessage
	fn            reflect.Value
	inputType     reflect.Type
	hasCtx        bool
	returnsValue  bool
	returnsString bool
}

// NewFunction wraps fn into a Tool named name.
func NewFunction(name, description string, fn any) (*FunctionTool, error) {
	rv := reflect.ValueOf(fn)
	if !rv.IsValid() || rv.Kind() != reflect.Func {
		return nil, fmt.Errorf("tool: %q: fn must be a function", name)
	}
	t := rv.Type()

	// Inputs: optional context.Context followed by exactly one struct argument.
	numIn := t.NumIn()
	hasCtx := false
	if numIn > 0 && t.In(0) == ctxType {
		hasCtx = true
	}
	if numIn-(boolToInt(hasCtx)) != 1 {
		return nil, fmt.Errorf("tool: %q: must take exactly one input parameter (plus optional context.Context)", name)
	}
	argIdx := 0
	if hasCtx {
		argIdx = 1
	}
	inputType := t.In(argIdx)
	if inputType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("tool: %q: input parameter must be a struct, got %s", name, inputType)
	}

	// Outputs: optional value followed by a mandatory error.
	numOut := t.NumOut()
	if numOut == 0 || numOut > 2 || !t.Out(numOut-1).Implements(errType) {
		return nil, fmt.Errorf("tool: %q: must return (T, error) or (error)", name)
	}
	returnsValue := numOut == 2
	returnsString := false
	if returnsValue {
		returnsString = t.Out(0).Kind() == reflect.String
	}

	schema, err := SchemaFromType(inputType)
	if err != nil {
		return nil, fmt.Errorf("tool: %q: %w", name, err)
	}

	return &FunctionTool{
		name:          name,
		description:   description,
		schema:        schema,
		fn:            rv,
		inputType:     inputType,
		hasCtx:        hasCtx,
		returnsValue:  returnsValue,
		returnsString: returnsString,
	}, nil
}

// Spec implements Tool.
func (f *FunctionTool) Spec() model.ToolParam {
	return model.ToolParam{
		Type: "function",
		Function: model.FunctionDef{
			Name:        f.name,
			Description: f.description,
			Parameters:  f.schema,
		},
	}
}

// Run implements Tool.
func (f *FunctionTool) Run(ctx context.Context, argumentsJSON string) (string, error) {
	if argumentsJSON == "" {
		argumentsJSON = "{}"
	}
	in := reflect.New(f.inputType)
	if err := json.Unmarshal([]byte(argumentsJSON), in.Interface()); err != nil {
		return "", fmt.Errorf("tool %s: invalid arguments: %w", f.name, err)
	}

	callArgs := make([]reflect.Value, 0, 2)
	if f.hasCtx {
		callArgs = append(callArgs, reflect.ValueOf(ctx))
	}
	callArgs = append(callArgs, in.Elem())

	out := f.fn.Call(callArgs)
	if errVal := out[len(out)-1]; !errVal.IsNil() {
		return "", errVal.Interface().(error)
	}
	if !f.returnsValue {
		return "", nil
	}
	v := out[0]
	if f.returnsString {
		return v.String(), nil
	}
	data, err := json.Marshal(v.Interface())
	if err != nil {
		return "", fmt.Errorf("tool %s: marshal result: %w", f.name, err)
	}
	return string(data), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
