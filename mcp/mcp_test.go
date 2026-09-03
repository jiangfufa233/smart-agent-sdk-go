package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/goleak"

	"github.com/jiangfufa233/smart-agent-sdk-go/tool"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// --- test server -----------------------------------------------------------

type addIn struct {
	A int `json:"a" desc:"first addend"`
	B int `json:"b" desc:"second addend"`
}

type addOut struct {
	Sum int `json:"sum"`
}

type shoutIn struct {
	Text string `json:"text"`
}

// newTestServer returns an MCP server exposing:
//   - add: typed tool with structured output  -> {"sum": a+b}
//   - shout: text-only tool                   -> "LOUD: <text>"
//   - boom: always returns a tool error
func newTestServer() *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "add", Description: "add two numbers"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in addIn) (*mcpsdk.CallToolResult, addOut, error) {
			return nil, addOut{Sum: in.A + in.B}, nil
		})
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "shout", Description: "shout text back"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in shoutIn) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "LOUD: " + in.Text}}}, nil, nil
		})
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "boom", Description: "always fails"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, any, error) {
			return nil, nil, errors.New("exploded")
		})
	return server
}

// startInMemory wires a test server to a Client through in-memory transports.
func startInMemory(t *testing.T, cfg Config) (*Client, *mcpsdk.ServerSession) {
	t.Helper()
	server := newTestServer()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	sdkClient := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := sdkClient.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{cfg: cfg, session: session}
	t.Cleanup(func() { _ = c.Close() })
	return c, serverSession
}

func findTool(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	for _, tt := range tools {
		if tt.Spec().Function.Name == name {
			return tt
		}
	}
	t.Fatalf("tool %q not found in %d tools", name, len(tools))
	return nil
}

// --- config validation ------------------------------------------------------

func TestNewClientValidation(t *testing.T) {
	if _, err := NewClient(Config{}); err == nil || !strings.Contains(err.Error(), "unknown transport") {
		t.Errorf("empty config: got %v", err)
	}
	if _, err := NewClient(Config{Transport: TransportStdio}); err == nil || !strings.Contains(err.Error(), "requires Command") {
		t.Errorf("stdio without command: got %v", err)
	}
	if _, err := NewClient(Config{Transport: TransportHTTP}); err == nil || !strings.Contains(err.Error(), "requires URL") {
		t.Errorf("http without URL: got %v", err)
	}
	if _, err := NewClient(Config{Transport: "carrier-pigeon", Command: "x"}); err == nil {
		t.Error("unknown transport accepted")
	}
}

func TestToolsBeforeConnect(t *testing.T) {
	c, err := NewClient(Config{Transport: TransportStdio, Command: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Tools(t.Context()); !errors.Is(err, ErrNotConnected) {
		t.Errorf("expected ErrNotConnected, got %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close before Connect: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close must be idempotent: %v", err)
	}
	if err := c.Connect(t.Context()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("Connect after Close: got %v", err)
	}
}

// --- in-memory full stack ---------------------------------------------------

func TestInMemoryToolsSpec(t *testing.T) {
	c, _ := startInMemory(t, Config{Transport: TransportHTTP})
	tools, err := c.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 3 {
		t.Fatalf("got %d tools, want 3", len(tools))
	}
	add := findTool(t, tools, "add").Spec()
	if add.Type != "function" {
		t.Errorf("spec type = %q", add.Type)
	}
	if add.Function.Description != "add two numbers" {
		t.Errorf("description = %q", add.Function.Description)
	}
	schema := string(add.Function.Parameters)
	for _, want := range []string{`"properties"`, `"a"`, `"b"`, `"required"`} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema missing %s: %s", want, schema)
		}
	}
}

func TestInMemoryRun(t *testing.T) {
	c, _ := startInMemory(t, Config{Transport: TransportHTTP})
	tools, err := c.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// Typed tool with structured output: flattened to its JSON.
	out, err := findTool(t, tools, "add").Run(t.Context(), `{"a":40,"b":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"sum":42`) {
		t.Errorf("add output = %q, want structured sum", out)
	}

	// Text content passthrough.
	out, err = findTool(t, tools, "shout").Run(t.Context(), `{"text":"hi"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "LOUD: hi" {
		t.Errorf("shout output = %q", out)
	}

	// Empty arguments become {}; boom takes no input, so this proves the
	// mapping reaches the handler (its error comes back as IsError).
	_, err = findTool(t, tools, "boom").Run(t.Context(), "")
	if err == nil || !strings.Contains(err.Error(), "exploded") {
		t.Errorf("boom with empty args: got %v, want server-side tool error", err)
	}
}

func TestInMemoryRunIsError(t *testing.T) {
	c, _ := startInMemory(t, Config{Transport: TransportHTTP})
	tools, err := c.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = findTool(t, tools, "boom").Run(t.Context(), "")
	if err == nil {
		t.Fatal("expected error for IsError result")
	}
	if !strings.Contains(err.Error(), "exploded") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should carry tool name and server message, got: %v", err)
	}
}

func TestInMemoryInvalidArguments(t *testing.T) {
	c, _ := startInMemory(t, Config{Transport: TransportHTTP})
	tools, err := c.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = findTool(t, tools, "add").Run(t.Context(), `{"a":"not-an-int"}`)
	if err == nil {
		t.Fatal("expected validation error from typed tool")
	}
}

func TestCloseThenRun(t *testing.T) {
	c, ss := startInMemory(t, Config{Transport: TransportHTTP})
	tools, err := c.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	add := findTool(t, tools, "add")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := add.Run(t.Context(), `{"a":1,"b":1}`); err == nil {
		t.Fatal("expected error after Close")
	}
	_ = ss.Close()
}

// --- policy integration -----------------------------------------------------

func TestPolicyIntegration(t *testing.T) {
	c, _ := startInMemory(t, Config{Transport: TransportHTTP, Policy: tool.Denylist("add")})
	tools, err := c.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 3 {
		t.Fatalf("policy must not hide tools from the model, got %d", len(tools))
	}
	_, err = findTool(t, tools, "add").Run(t.Context(), `{"a":1,"b":1}`)
	var authErr *tool.AuthorizationError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *tool.AuthorizationError, got %v", err)
	}
	if !strings.Contains(err.Error(), "denied by policy") {
		t.Errorf("denial message = %v", err)
	}
	if _, err := findTool(t, tools, "shout").Run(t.Context(), `{"text":"ok"}`); err != nil {
		t.Errorf("non-denied tool should pass policy: %v", err)
	}
}

// --- stdio transport ---------------------------------------------------------

func TestStdioCommandNotFound(t *testing.T) {
	c, err := NewClient(Config{Transport: TransportStdio, Command: "definitely-not-a-real-binary-xyz"})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Connect(t.Context())
	if err == nil || !strings.Contains(err.Error(), "connect via stdio") {
		t.Fatalf("expected connect error, got %v", err)
	}
	_ = c.Close()
}

func TestStdioImmediateExit(t *testing.T) {
	// The process exits before the handshake completes.
	c, err := NewClient(Config{Transport: TransportStdio, Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(t.Context()); err == nil {
		t.Fatal("expected handshake failure for immediately-exiting process")
	}
	_ = c.Close()
}

// TestStdioEndToEnd spawns the test binary itself as a real MCP server
// speaking newline-delimited JSON over stdio pipes.
func TestStdioEndToEnd(t *testing.T) {
	if os.Getenv(mcpHelperEnv) != "" {
		return // this test only acts as the parent
	}
	t.Setenv(mcpHelperEnv, "1")
	c, err := NewClient(Config{
		Transport: TransportStdio,
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestMCPHelperProcess", "--"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	tools, err := c.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out, err := findTool(t, tools, "add").Run(t.Context(), `{"a":19,"b":23}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"sum":42`) {
		t.Errorf("stdio add output = %q", out)
	}
	if _, err := findTool(t, tools, "boom").Run(t.Context(), ""); err == nil {
		t.Error("expected boom to fail over stdio too")
	}
}

const mcpHelperEnv = "SMART_AGENT_SDK_MCP_HELPER"

// TestMCPHelperProcess runs a real MCP server over stdin/stdout. It is
// re-executed by TestStdioEndToEnd with mcpHelperEnv set.
func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv(mcpHelperEnv) == "" {
		t.Skip("helper process, not meant to run directly")
	}
	server := newTestServer()
	if err := server.Run(t.Context(), &mcpsdk.StdioTransport{}); err != nil {
		t.Errorf("server run: %v", err)
	}
	// Terminate the test process without printing the usual test output to
	// stdout, which would corrupt the JSON-RPC stream.
	os.Exit(0)
}

// --- streamable-http transport ----------------------------------------------

func TestHTTPEndToEnd(t *testing.T) {
	server := newTestServer()
	ts := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return server
	}, nil))
	t.Cleanup(func() {
		// End server-side sessions first so the handler's connection
		// goroutines exit before the listener goes away.
		for ss := range server.Sessions() {
			_ = ss.Close()
		}
		ts.Close()
	})

	c, err := NewClient(Config{Transport: TransportHTTP, URL: ts.URL, DisableStandaloneSSE: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	tools, err := c.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out, err := findTool(t, tools, "add").Run(t.Context(), `{"a":40,"b":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"sum":42`) {
		t.Errorf("http add output = %q", out)
	}
}

func TestHTTPRefusedConnection(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close() // nothing listens there anymore

	c, err := NewClient(Config{Transport: TransportHTTP, URL: url, DisableStandaloneSSE: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(t.Context()); err == nil {
		t.Fatal("expected connect error for dead endpoint")
	}
	_ = c.Close()
}

func TestCallToolContextTimeout(t *testing.T) {
	c, _ := startInMemory(t, Config{Transport: TransportHTTP})
	tools, err := c.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)
	if _, err := findTool(t, tools, "add").Run(ctx, `{"a":1,"b":1}`); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}
