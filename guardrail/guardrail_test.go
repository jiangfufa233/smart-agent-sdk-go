package guardrail

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/jiangfufa233/smart-agent-sdk-go/agent"
)

func evalInput(t *testing.T, g agent.InputGuardrail, input string) (agent.GuardrailResult, error) {
	t.Helper()
	return g.Guardrail(context.Background(), &agent.Agent{Name: "a"}, input)
}

func TestDenyPatterns(t *testing.T) {
	g := DenyPatterns("secrets",
		regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	)

	res, err := evalInput(t, g, "what is the weather?")
	if err != nil || res.Tripwire {
		t.Fatalf("res = %+v, err = %v; want pass", res, err)
	}

	res, err = evalInput(t, g, "my key is sk-abcdefghij0123456789 ok")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Tripwire {
		t.Fatalf("res = %+v, want trip", res)
	}
	info, ok := res.Info.(map[string]any)
	if !ok {
		t.Fatalf("info = %v, want map", res.Info)
	}
	if info["pattern"] != `sk-[A-Za-z0-9]{20,}` {
		t.Fatalf("pattern = %v", info["pattern"])
	}
	if info["match_offset"] != 10 || info["match_len"] != 23 {
		t.Fatalf("match info = %v", info)
	}
	for k, v := range info {
		if s, isStr := v.(string); isStr && strings.Contains(s, "sk-abcdefghij") {
			t.Fatalf("%s echoes matched secret text: %v", k, info)
		}
	}

	// First matching pattern in declaration order wins.
	res, _ = evalInput(t, g, "-----BEGIN RSA PRIVATE KEY-----")
	if !res.Tripwire {
		t.Fatal("private key pattern did not trip")
	}
}

func TestDenyPatternsSkipsNil(t *testing.T) {
	g := DenyPatterns("nil-safe", nil)
	res, err := evalInput(t, g, "anything")
	if err != nil || res.Tripwire {
		t.Fatalf("res = %+v, err = %v; want pass", res, err)
	}
}

func TestMaxLength(t *testing.T) {
	g := MaxLength(5)
	if g.Name != "max_length_5" {
		t.Fatalf("name = %q", g.Name)
	}

	res, err := evalInput(t, g, "你好世")
	if err != nil || res.Tripwire {
		t.Fatalf("res = %+v, err = %v; want pass at boundary", res, err)
	}

	res, err = evalInput(t, g, "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Tripwire {
		t.Fatalf("res = %+v, want trip", res)
	}
	info := res.Info.(map[string]any)
	if info["length"] != 11 || info["max"] != 5 {
		t.Fatalf("info = %v", info)
	}
}
