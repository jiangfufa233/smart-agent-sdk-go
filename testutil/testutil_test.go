package testutil

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
)

func TestScriptedReplaysInOrder(t *testing.T) {
	s := NewScripted(TextStep("one"), TextStep("two"))
	for i, want := range []string{"one", "two"} {
		res, err := s.Chat(context.Background(), &model.Request{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Message.Content != want {
			t.Fatalf("step %d = %q, want %q", i, res.Message.Content, want)
		}
	}
	if _, err := s.Chat(context.Background(), &model.Request{}); !errors.Is(err, ErrScriptExhausted) {
		t.Fatalf("expected ErrScriptExhausted, got %v", err)
	}
	if s.Calls() != 3 {
		t.Fatalf("calls = %d, want 3", s.Calls())
	}
}

func TestScriptedRecordsRequestsIndependently(t *testing.T) {
	s := NewScripted(TextStep("ok"))
	req := &model.Request{Model: "m", Messages: []model.Message{{Role: model.RoleUser, Content: "first"}}}
	if _, err := s.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.Messages[0].Content = "mutated" // must not affect the recording
	rec := s.Requests()[0]
	if rec.Messages[0].Content != "first" {
		t.Fatalf("recording aliased the caller's slice: %+v", rec.Messages)
	}
	if s.LastRequest() != rec {
		t.Fatal("LastRequest mismatch")
	}
}

func TestScriptedFuncAndDelay(t *testing.T) {
	s := NewScripted(Step{
		Delay: 5 * time.Millisecond,
		Func: func(req *model.Request) (*model.Response, error) {
			return TextStep("from func").Resp, nil
		},
	})
	start := time.Now()
	res, err := s.Chat(context.Background(), &model.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Message.Content != "from func" {
		t.Fatalf("out = %q", res.Message.Content)
	}
	if time.Since(start) < 5*time.Millisecond {
		t.Fatal("delay not applied")
	}
}

func TestScriptedErrorStep(t *testing.T) {
	s := NewScripted(HTTPErrorStep(429, "slow"))
	_, err := s.Chat(context.Background(), &model.Request{})
	var me *model.ModelError
	if !errors.As(err, &me) || !me.Retryable || me.Kind != model.ErrorRateLimited {
		t.Fatalf("HTTP error step not classified: %v", err)
	}
}
