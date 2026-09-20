package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/goleak"
	"golang.org/x/oauth2"

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

type httpHeaderRecorder struct {
	mu      sync.Mutex
	headers []http.Header
}

func (r *httpHeaderRecorder) record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.headers = append(r.headers, req.Header.Clone())
}

func (r *httpHeaderRecorder) snapshot() []http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]http.Header(nil), r.headers...)
}

func newObservedHTTPServer(t *testing.T, recorder *httpHeaderRecorder) *httptest.Server {
	t.Helper()
	server := newTestServer()
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return server
	}, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		recorder.record(req)
		handler.ServeHTTP(w, req)
	}))
	t.Cleanup(func() {
		for ss := range server.Sessions() {
			_ = ss.Close()
		}
		ts.Close()
	})
	return ts
}

type headerProviderContextKey struct{}

func TestHTTPStaticAndDynamicHeaders(t *testing.T) {
	recorder := &httpHeaderRecorder{}
	ts := newObservedHTTPServer(t, recorder)

	headers := http.Header{
		"X-Static":      {"configured"},
		"X-Multi":       {"one", "two"},
		"Authorization": {"Bearer ${token}"},
		"X-User":        {"{username}"},
		"X-Group":       {"{group}"},
	}
	callerClient := &http.Client{}
	var providerMu sync.Mutex
	providerCalls := 0
	providerValues := HeaderValues{Username: "alice", Group: "devs", Token: "dynamic-token"}
	c, err := NewClient(Config{
		Transport:            TransportHTTP,
		URL:                  ts.URL,
		HTTPClient:           callerClient,
		HTTPHeaders:          headers,
		DisableStandaloneSSE: true,
		HeaderProvider: func(ctx context.Context) (HeaderValues, error) {
			if got, _ := ctx.Value(headerProviderContextKey{}).(string); got != "request-context" {
				t.Errorf("provider context = %q, want request-context", got)
			}
			providerMu.Lock()
			providerCalls++
			providerMu.Unlock()
			return providerValues, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// NewClient owns a clone rather than the caller's header map.
	headers.Set("X-Static", "caller-mutated")
	ctx := context.WithValue(t.Context(), headerProviderContextKey{}, "request-context")
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := c.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := findTool(t, tools, "add").Run(ctx, `{"a":20,"b":22}`); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	rec := recorder.snapshot()
	if len(rec) == 0 {
		t.Fatal("server did not receive any requests")
	}
	providerMu.Lock()
	calls := providerCalls
	providerMu.Unlock()
	if calls != len(rec) {
		t.Errorf("provider calls = %d, server requests = %d", calls, len(rec))
	}
	for i, got := range rec {
		if value := got.Get("X-Static"); value != "configured" {
			t.Errorf("request %d X-Static = %q, want configured", i, value)
		}
		if values := got.Values("X-Multi"); len(values) != 2 || values[0] != "one" || values[1] != "two" {
			t.Errorf("request %d X-Multi = %q, want [one two]", i, values)
		}
		if value := got.Get("Authorization"); value != "Bearer dynamic-token" {
			t.Errorf("request %d Authorization = %q, want rendered token template", i, value)
		}
		if value := got.Get("X-User"); value != "alice" {
			t.Errorf("request %d X-User = %q", i, value)
		}
		if value := got.Get("X-Group"); value != "devs" {
			t.Errorf("request %d X-Group = %q", i, value)
		}
	}
	if providerValues != (HeaderValues{Username: "alice", Group: "devs", Token: "dynamic-token"}) {
		t.Errorf("provider values were mutated: %+v", providerValues)
	}
	if callerClient.Transport != nil {
		t.Error("NewClient mutated the caller's HTTP client transport")
	}
}

func TestHTTPHeaderProviderRotationAcrossReconnect(t *testing.T) {
	recorder := &httpHeaderRecorder{}
	ts := newObservedHTTPServer(t, recorder)

	var tokenMu sync.Mutex
	token := "first"
	provider := func(context.Context) (HeaderValues, error) {
		tokenMu.Lock()
		defer tokenMu.Unlock()
		return HeaderValues{Token: token}, nil
	}
	c, err := NewClient(Config{
		Transport:            TransportHTTP,
		URL:                  ts.URL,
		HTTPHeaders:          http.Header{"X-Static": {"static"}, "Authorization": {"Bearer ${token}"}},
		HeaderProvider:       provider,
		DisableStandaloneSSE: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	assertHeaders := func(from int, wantToken string) {
		t.Helper()
		headers := recorder.snapshot()
		if len(headers) == from {
			t.Fatalf("no requests recorded after index %d", from)
		}
		for i, got := range headers[from:] {
			if value := got.Get("X-Static"); value != "static" {
				t.Errorf("request %d X-Static = %q", from+i, value)
			}
			if value := got.Get("Authorization"); value != "Bearer "+wantToken {
				t.Errorf("request %d Authorization = %q, want token %q", from+i, value, wantToken)
			}
		}
	}
	setToken := func(value string) {
		tokenMu.Lock()
		token = value
		tokenMu.Unlock()
	}

	if err := c.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	firstEnd := len(recorder.snapshot())
	if firstEnd == 0 {
		t.Fatal("connect did not make a request")
	}
	assertHeaders(0, "first")

	setToken("second")
	if _, err := c.Tools(t.Context()); err != nil {
		t.Fatal(err)
	}
	secondEnd := len(recorder.snapshot())
	assertHeaders(firstEnd, "second")

	setToken("third")
	if err := c.Reconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertHeaders(secondEnd, "third")
	reconnectEnd := len(recorder.snapshot())
	if _, err := c.Tools(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertHeaders(reconnectEnd, "third")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHTTPHeaderTransportClonesRequestAndHeaderMaps(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://example.test", nil)
	request.Header.Set("X-Original", "keep")
	staticHeaders := http.Header{"X-Static": {"static"}, "Authorization": {"Bearer ${token}"}}
	var received *http.Request
	transport := &headerTransport{
		staticHeaders: staticHeaders,
		headerProvider: func(context.Context) (HeaderValues, error) {
			return HeaderValues{Token: "dynamic-token"}, nil
		},
		base: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			received = req
			req.Header.Set("X-Transport", "mutated-clone")
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
		}),
	}

	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if received == request {
		t.Error("underlying RoundTripper received the caller's request")
	}
	if value := request.Header.Get("X-Original"); value != "keep" {
		t.Errorf("original request header = %q, want keep", value)
	}
	if value := request.Header.Get("X-Static"); value != "" {
		t.Errorf("static header mutated original request: %q", value)
	}
	if value := request.Header.Get("Authorization"); value != "" {
		t.Errorf("provider header mutated original request: %q", value)
	}
	if value := request.Header.Get("X-Transport"); value != "" {
		t.Errorf("base transport mutation reached original request: %q", value)
	}
	if value := received.Header.Get("X-Static"); value != "static" {
		t.Errorf("received static header = %q", value)
	}
	if value := received.Header.Get("Authorization"); value != "Bearer dynamic-token" {
		t.Errorf("received dynamic header = %q", value)
	}
	if value := staticHeaders.Get("Authorization"); value != "Bearer ${token}" {
		t.Errorf("static header map was mutated: %q", value)
	}
}

func TestHTTPHeaderProviderFailurePreventsRequestsAndRedactsSecret(t *testing.T) {
	secret := "Bearer rendered-secret-value"
	var requestMu sync.Mutex
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestMu.Lock()
		requests++
		requestMu.Unlock()
	}))
	defer ts.Close()

	c, err := NewClient(Config{
		Transport:            TransportHTTP,
		URL:                  ts.URL,
		DisableStandaloneSSE: true,
		HeaderProvider: func(context.Context) (HeaderValues, error) {
			return HeaderValues{}, errors.New("credential refresh failed for " + secret)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	err = c.Connect(t.Context())
	if err == nil {
		t.Fatal("Connect succeeded with a failing header provider")
	}
	if !errors.Is(err, ErrHeaderProvider) {
		t.Errorf("Connect error = %v, want ErrHeaderProvider", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("Connect error exposed secret: %v", err)
	}
	requestMu.Lock()
	gotRequests := requests
	requestMu.Unlock()
	if gotRequests != 0 {
		t.Errorf("server received %d requests after provider failure", gotRequests)
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

// --- header template authentication -----------------------------------------

type stubOAuthHandler struct{}

func (stubOAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) { return nil, nil }
func (stubOAuthHandler) Authorize(context.Context, *http.Request, *http.Response) error {
	return nil
}

type staticOAuthHandler struct {
	token string
}

func (h staticOAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: h.token, TokenType: "Bearer"}), nil
}

func (staticOAuthHandler) Authorize(context.Context, *http.Request, *http.Response) error {
	return errors.New("authorize must not be called while TokenSource supplies a token")
}

func TestHeaderTemplateValidation(t *testing.T) {
	base := func(headers http.Header) Config {
		return Config{Transport: TransportHTTP, URL: "http://example.test", HTTPHeaders: headers}
	}
	_, err := NewClient(base(http.Header{"X-Bad": {"Bearer {unknown}"}}))
	if !errors.Is(err, ErrHeaderTemplate) {
		t.Errorf("unknown template variable: got %v", err)
	}
	_, err = NewClient(base(http.Header{"Authorization": {"Bearer ${token}"}}))
	if !errors.Is(err, ErrHeaderTemplate) {
		t.Errorf("dynamic template without provider: got %v", err)
	}
	_, err = NewClient(Config{
		Transport:    TransportHTTP,
		URL:          "http://example.test",
		HTTPHeaders:  http.Header{"Authorization": {"Bearer static"}},
		OAuthHandler: stubOAuthHandler{},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Errorf("OAuthHandler + Authorization header: got %v", err)
	}
	_, err = NewClient(base(http.Header{"Authorization": {"Bearer ${token}"}, "X-User": {"{username}"}}))
	if !errors.Is(err, ErrHeaderTemplate) {
		t.Errorf("one dynamic header still requires a provider: got %v", err)
	}
	c, err := NewClient(Config{
		Transport:      TransportHTTP,
		URL:            "http://example.test",
		HTTPHeaders:    http.Header{"Authorization": {"Bearer ${token}"}, "X-Literal": {"static-value"}},
		HeaderProvider: func(context.Context) (HeaderValues, error) { return HeaderValues{Token: "t"}, nil },
	})
	if err != nil {
		t.Errorf("valid templated config rejected: %v", err)
	}
	_ = c
}

func TestRenderHeaderTemplate(t *testing.T) {
	tests := []struct {
		name     string
		template string
		values   *HeaderValues
		want     string
		dynamic  bool
		err      error
	}{
		{name: "static", template: "Bearer static", values: nil, want: "Bearer static"},
		{name: "token", template: "Bearer ${token}", values: &HeaderValues{Token: "t"}, want: "Bearer t", dynamic: true},
		{name: "user and group", template: "{username}/{group}", values: &HeaderValues{Username: "u", Group: "g"}, want: "u/g", dynamic: true},
		{name: "mixed", template: "${token}:{username}", values: &HeaderValues{Username: "u", Token: "t"}, want: "t:u", dynamic: true},
		{name: "empty token with values", template: "Bearer ${token}", values: &HeaderValues{}, want: "", dynamic: true, err: ErrHeaderProvider},
		{name: "empty user with values", template: "{username}", values: &HeaderValues{}, want: "", dynamic: true, err: ErrHeaderProvider},
		{name: "unknown variable", template: "Bearer {unknown}", values: &HeaderValues{}, want: "", err: ErrHeaderTemplate},
		{name: "unknown dollar variable", template: "Bearer ${unknown}", values: &HeaderValues{}, want: "", err: ErrHeaderTemplate},
		{name: "unclosed variable", template: "Bearer {username", values: &HeaderValues{}, want: "", err: ErrHeaderTemplate},
		{name: "validation ignores empty values", template: "Bearer ${token}", values: nil, want: "Bearer ", dynamic: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, dynamic, err := renderHeaderTemplate(tt.template, tt.values)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
			if got != tt.want {
				t.Errorf("rendered = %q, want %q", got, tt.want)
			}
			if dynamic != tt.dynamic {
				t.Errorf("dynamic = %t, want %t", dynamic, tt.dynamic)
			}
		})
	}
}

func TestHeaderTransportRefusesCrossOrigin(t *testing.T) {
	endpoint, _ := url.Parse("http://origin.test/mcp")
	var baseCalled bool
	transport := &headerTransport{
		endpoint: endpoint,
		base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			baseCalled = true
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
		}),
	}

	req := httptest.NewRequest(http.MethodGet, "http://other.test/mcp", nil)
	if _, err := transport.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "cross-origin") {
		t.Fatalf("cross-origin request: got %v, want refusal", err)
	}
	if baseCalled {
		t.Error("cross-origin request reached the base transport")
	}

	sameOrigin := httptest.NewRequest(http.MethodGet, "http://origin.test/other", nil)
	resp, err := transport.RoundTrip(sameOrigin)
	if err != nil {
		t.Fatalf("same-origin request: %v", err)
	}
	_ = resp.Body.Close()
}

func TestHTTPRedirectCrossOriginRefused(t *testing.T) {
	var targetMu sync.Mutex
	targetRequests := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetMu.Lock()
		targetRequests++
		targetMu.Unlock()
	}))
	defer target.Close()

	var originRequests int
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originRequests++
		http.Redirect(w, r, target.URL+"/redirected", http.StatusFound)
	}))
	defer origin.Close()

	c, err := NewClient(Config{
		Transport:            TransportHTTP,
		URL:                  origin.URL,
		HTTPHeaders:          http.Header{"Authorization": {"Bearer ${token}"}},
		HeaderProvider:       func(context.Context) (HeaderValues, error) { return HeaderValues{Token: "secret"}, nil },
		DisableStandaloneSSE: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	err = c.Connect(t.Context())
	if err == nil || !strings.Contains(err.Error(), "cross-origin") {
		t.Fatalf("redirected connect: got %v, want cross-origin refusal", err)
	}
	targetMu.Lock()
	hits := targetRequests
	targetMu.Unlock()
	if hits != 0 {
		t.Errorf("redirect target received %d requests", hits)
	}
	if originRequests == 0 {
		t.Error("origin server was never contacted")
	}
}

func TestHTTPOAuthHandlerPassthrough(t *testing.T) {
	var authMu sync.Mutex
	authHeaders := map[string]int{}
	server := newTestServer()
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		auth := req.Header.Get("Authorization")
		authMu.Lock()
		authHeaders[auth]++
		authMu.Unlock()
		if auth != "Bearer oauth-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, req)
	}))
	t.Cleanup(func() {
		for ss := range server.Sessions() {
			_ = ss.Close()
		}
		ts.Close()
	})

	c, err := NewClient(Config{
		Transport:            TransportHTTP,
		URL:                  ts.URL,
		OAuthHandler:         staticOAuthHandler{token: "oauth-secret"},
		DisableStandaloneSSE: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	if err := c.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tools, err := c.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out, err := findTool(t, tools, "add").Run(t.Context(), `{"a":1,"b":1}`)
	if err != nil || !strings.Contains(out, `"sum":2`) {
		t.Fatalf("authorized tool call = (%q, %v)", out, err)
	}
	authMu.Lock()
	defer authMu.Unlock()
	if authHeaders["Bearer oauth-secret"] == 0 {
		t.Errorf("server never saw the OAuth bearer token: %v", authHeaders)
	}
}
