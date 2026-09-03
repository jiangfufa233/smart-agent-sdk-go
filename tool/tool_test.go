package tool

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type simpleArgs struct {
	Query string `json:"query" desc:"the query"`
	Limit int    `json:"limit,omitempty"`
}

func TestNewFunctionValidation(t *testing.T) {
	cases := []struct {
		name string
		fn   any
	}{
		{"not a function", "nope"},
		{"no error return", func(in simpleArgs) string { return "" }},
		{"too many inputs", func(a simpleArgs, b simpleArgs) (string, error) { return "", nil }},
		{"no inputs", func() (string, error) { return "", nil }},
		{"non-struct input", func(s string) (string, error) { return "", nil }},
	}
	for _, tc := range cases {
		if _, err := NewFunction("t", "d", tc.fn); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

func TestRunStringResult(t *testing.T) {
	ft, err := NewFunction("search", "search",
		func(ctx context.Context, in simpleArgs) (string, error) {
			return "results for " + in.Query, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ft.Run(context.Background(), `{"query":"golang"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "results for golang" {
		t.Fatalf("out = %q", out)
	}
}

func TestRunValueResultMarshals(t *testing.T) {
	ft, err := NewFunction("stats", "stats",
		func(ctx context.Context, in simpleArgs) (map[string]int, error) {
			return map[string]int{"limit": in.Limit}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ft.Run(context.Background(), `{"query":"x","limit":3}`)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["limit"] != 3 {
		t.Fatalf("got %v", got)
	}
}

func TestRunInvalidJSON(t *testing.T) {
	ft, err := NewFunction("t", "d",
		func(ctx context.Context, in simpleArgs) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ft.Run(context.Background(), `{bad`); err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("expected invalid arguments error, got %v", err)
	}
}

func TestRunToolErrorPropagates(t *testing.T) {
	sentinel := errors.New("nope")
	ft, err := NewFunction("t", "d",
		func(ctx context.Context, in simpleArgs) (string, error) { return "", sentinel })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ft.Run(context.Background(), `{}`); !errors.Is(err, sentinel) {
		t.Fatalf("sentinel lost: %v", err)
	}
}

func TestRunEmptyArguments(t *testing.T) {
	ft, err := NewFunction("t", "d",
		func(ctx context.Context, in struct{}) (string, error) { return "ok", nil })
	if err != nil {
		t.Fatal(err)
	}
	if out, err := ft.Run(context.Background(), ""); err != nil || out != "ok" {
		t.Fatalf("empty args should default to {}: %q, %v", out, err)
	}
}

func TestSpec(t *testing.T) {
	ft, err := NewFunction("search", "search things",
		func(ctx context.Context, in simpleArgs) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	spec := ft.Spec()
	if spec.Type != "function" || spec.Function.Name != "search" {
		t.Fatalf("spec = %+v", spec)
	}
	if !strings.Contains(spec.Function.Description, "search things") {
		t.Fatalf("description missing: %+v", spec.Function)
	}
}

func TestSchemaFromType(t *testing.T) {
	type inner struct {
		Name string `json:"name"`
	}
	type full struct {
		A       string
		B       int      `json:"b,omitempty"`
		C       []string `desc:"list of c"`
		D       time.Time
		P       *string
		Inner   inner  `json:"inner"`
		skipped string //nolint:structcheck,unused
	}
	s, err := SchemaFromType(reflect.TypeOf(full{}))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(s, &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "object" {
		t.Fatalf("type = %v", m["type"])
	}
	props := m["properties"].(map[string]any)
	for _, key := range []string{"A", "b", "C", "D", "P", "inner"} {
		if _, ok := props[key]; !ok {
			t.Errorf("property %s missing: %s", key, s)
		}
	}
	if _, ok := props["skipped"]; ok {
		t.Error("unexported field must be skipped")
	}
	if c := props["C"].(map[string]any); c["description"] != "list of c" {
		t.Errorf("desc tag missing: %v", c)
	}
	if d := props["D"].(map[string]any); d["format"] != "date-time" {
		t.Errorf("time.Time mapping wrong: %v", d)
	}
	req := m["required"].([]any)
	// A, C, D, inner are required (b/P are omitempty/pointer)
	if len(req) != 4 {
		t.Errorf("required = %v", req)
	}
}

func BenchmarkSchemaFromType(b *testing.B) {
	type args struct {
		Query   string   `json:"query" desc:"the query"`
		Limit   int      `json:"limit,omitempty"`
		Tags    []string `json:"tags,omitempty"`
		Verbose bool     `json:"verbose"`
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := SchemaFromType(reflect.TypeOf(args{})); err != nil {
			b.Fatal(err)
		}
	}
}
