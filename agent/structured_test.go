package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/testutil"
)

type reportArgs struct {
	City        string  `json:"city"`
	Temperature float64 `json:"temperature" desc:"Temperature in Celsius"`
}

func TestRunTypedInjectsSchema(t *testing.T) {
	m := testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
		rf := req.ResponseFormat
		if rf == nil || rf.Type != model.FormatJSONSchema || rf.JSONSchema == nil {
			return nil, fmt.Errorf("response format not injected: %+v", req.Settings)
		}
		if !strings.Contains(string(rf.JSONSchema.Schema), `"city"`) {
			return nil, fmt.Errorf("schema missing fields: %s", rf.JSONSchema.Schema)
		}
		if rf.JSONSchema.Name != "reportArgs" {
			return nil, fmt.Errorf("schema name = %q", rf.JSONSchema.Name)
		}
		return testutil.TextStep(`{"city":"SF","temperature":21}`).Resp, nil
	}})
	a := &Agent{Name: "structured", Model: m}
	res, err := RunTyped[reportArgs](context.Background(), NewRunner(), a, "weather?")
	if err != nil {
		t.Fatal(err)
	}
	if res.Value.City != "SF" || res.Value.Temperature != 21 {
		t.Fatalf("value = %+v", res.Value)
	}
	if res.Result.Output != `{"city":"SF","temperature":21}` {
		t.Fatalf("output = %q", res.Result.Output)
	}
	if a.Settings != nil {
		t.Fatal("original agent settings must stay untouched")
	}
}

func TestRunTypedPreservesSettings(t *testing.T) {
	temp := 0.3
	a := &Agent{Name: "x", Model: testutil.NewScripted(testutil.TextStep(`{"city":"SF","temperature":1}`))}
	a.Settings = &model.Settings{Temperature: &temp}
	res, err := RunTyped[reportArgs](context.Background(), NewRunner(), a, "x")
	if err != nil {
		t.Fatal(err)
	}
	s := res.Result.Agent.Settings
	if s.Temperature == nil || *s.Temperature != 0.3 || s.ResponseFormat == nil {
		t.Fatalf("settings = %+v", s)
	}
	if a.Settings.ResponseFormat != nil {
		t.Fatal("original agent must stay untouched")
	}
}

func TestRunTypedKeepsExistingFormat(t *testing.T) {
	m := testutil.NewScripted(testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
		rf := req.ResponseFormat
		if rf == nil || rf.Type != model.FormatJSONObject || rf.JSONSchema != nil {
			return nil, fmt.Errorf("existing format overridden: %+v", rf)
		}
		return testutil.TextStep(`{"city":"SF","temperature":2}`).Resp, nil
	}})
	a := &Agent{Name: "x", Model: m}
	a.Settings = &model.Settings{ResponseFormat: &model.ResponseFormat{Type: model.FormatJSONObject}}
	if _, err := RunTyped[reportArgs](context.Background(), NewRunner(), a, "x"); err != nil {
		t.Fatal(err)
	}
}

func TestRunTypedStripsFence(t *testing.T) {
	res, err := RunTyped[reportArgs](context.Background(), NewRunner(),
		&Agent{Name: "x", Model: testutil.NewScripted(testutil.TextStep("```json\n{\"city\":\"SF\",\"temperature\":21}\n```"))}, "x")
	if err != nil {
		t.Fatal(err)
	}
	if res.Value.City != "SF" {
		t.Fatalf("value = %+v", res.Value)
	}
}

func TestRunTypedDecodeError(t *testing.T) {
	_, err := RunTyped[reportArgs](context.Background(), NewRunner(),
		&Agent{Name: "x", Model: testutil.NewScripted(testutil.TextStep("no json here"))}, "x")
	var se *StructuredOutputError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StructuredOutputError, got %v", err)
	}
	if se.Raw != "no json here" {
		t.Fatalf("raw = %q", se.Raw)
	}
}

func TestRunTypedUnsupportedType(t *testing.T) {
	_, err := RunTyped[chan int](context.Background(), NewRunner(), &Agent{Name: "x", Model: testutil.NewScripted(testutil.TextStep("{}"))}, "x")
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("err = %v", err)
	}
}

func TestStripCodeFence(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                          `{"a":1}`,
		"  \n {\"a\":1} \n":                `{"a":1}`,
		"```json\n{\"a\":1}\n```":          `{"a":1}`,
		"```\n{\"a\":1}\n```":              `{"a":1}`,
		"```json\n{\"a\":1}\n``` trailing": `{"a":1}`,
		"```json":                          "```json",
	}
	for in, want := range cases {
		if got := stripCodeFence(in); got != want {
			t.Errorf("stripCodeFence(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSchemaName(t *testing.T) {
	cases := map[string]string{
		schemaName(reflect.TypeFor[reportArgs]()): "reportArgs",
		schemaName(reflect.TypeFor[[]string]()):   "response", // slice has no name
		schemaName(reflect.TypeFor[chan int]()):   "response", // channel has no name
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("schemaName = %q, want %q", got, want)
		}
	}
	type inner struct{}
	if got := schemaName(reflect.TypeFor[inner]()); got != "inner" {
		t.Errorf("schemaName = %q", got)
	}
}
