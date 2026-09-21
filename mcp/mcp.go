// Package mcp integrates Model Context Protocol (MCP) servers as tool
// sources: it connects to a server over stdio or streamable-http, performs
// the initialize handshake, and adapts the server's tools to the SDK's
// tool.Tool interface so an agent can call them like any local tool.
//
// Usage:
//
//	c, err := mcp.NewClient(mcp.Config{
//		Transport: mcp.TransportStdio,
//		Command:   "npx",
//		Args:      []string{"-y", "@modelcontextprotocol/server-everything"},
//	})
//	if err != nil { ... }
//	if err := c.Connect(ctx); err != nil { ... }
//	defer c.Close()
//	tools, err := c.Tools(ctx)
//	...
//	agent.Tools = append(agent.Tools, tools...)
//
// The implementation is built on the official
// github.com/modelcontextprotocol/go-sdk. Remote tool calls may be guarded
// by an authorization policy via Config.Policy (see tool.Policy).
package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jiangfufa233/smart-agent-sdk-go/v2/tool"
)

// Transport selects how the MCP server is reached.
type Transport string

const (
	// TransportStdio launches the server as a child process and communicates
	// over its stdin/stdout using newline-delimited JSON.
	TransportStdio Transport = "stdio"
	// TransportHTTP connects to a streamable-http endpoint.
	TransportHTTP Transport = "streamable-http"
)

// ErrNotConnected is returned by Client.Tools when called before a
// successful Connect.
var ErrNotConnected = errors.New("mcp: client not connected")

// ErrHeaderProvider is returned when HeaderProvider cannot supply request
// headers. The provider's error is intentionally not exposed because it may
// contain a rendered credential.
var ErrHeaderProvider = errors.New("mcp: header provider failed")

var errInvalidHTTPHeaders = errors.New("mcp: invalid HTTP headers")
var ErrHeaderTemplate = errors.New("mcp: invalid HTTP header template")

// HeaderValues supplies the only supported template variables; referenced values must be nonempty.
type HeaderValues struct {
	Username string
	Group    string
	Token    string
}

// HeaderProvider runs for every HTTP request, possibly concurrently, with its request context.
type HeaderProvider func(context.Context) (HeaderValues, error)

// Config describes how to reach one MCP server.
type Config struct {
	Transport Transport
	// Command/Args are used with TransportStdio. The child process is
	// terminated when Client.Close is called.
	Command string
	Args    []string
	// URL is used with TransportHTTP.
	URL string

	// Name/Version identify this SDK during the MCP initialize handshake.
	// Version defaults to the tagged version of this module, or "dev" when
	// built from source.
	Name    string
	Version string

	// HTTPClient is used with TransportHTTP. nil means http.DefaultClient.
	HTTPClient *http.Client
	// HTTPHeaders are cloned and support only {username}, {group}, and ${token} templates.
	HTTPHeaders http.Header
	// HeaderProvider supplies template values on every request; failure prevents sending.
	HeaderProvider HeaderProvider
	// OAuthHandler is passed to the official SDK; it cannot coexist with an Authorization header.
	OAuthHandler auth.OAuthHandler
	// DisableStandaloneSSE applies to TransportHTTP: when true, no
	// standalone SSE stream is opened, so server-initiated messages (e.g.
	// tool list changes) are not received. Request-response still works.
	DisableStandaloneSSE bool

	// Policy, when set, authorizes every remote tool call before it is sent
	// to the server (see tool.Policy). Denials are reported back to the
	// model as tool errors without contacting the server.
	Policy tool.Policy
}

// Client is a connection to a single MCP server. It is safe for concurrent
// use once connected.
type Client struct {
	cfg Config

	mu         sync.Mutex
	session    *mcpsdk.ClientSession
	procCancel context.CancelFunc
	closed     bool
}

// NewClient creates a client for the server described by cfg. It performs no
// I/O; call Connect to start the transport and complete the MCP handshake.
func NewClient(cfg Config) (*Client, error) {
	switch cfg.Transport {
	case TransportStdio:
		if cfg.Command == "" {
			return nil, fmt.Errorf("mcp: stdio transport requires Command")
		}
	case TransportHTTP:
		if cfg.URL == "" {
			return nil, fmt.Errorf("mcp: streamable-http transport requires URL")
		}
	default:
		return nil, fmt.Errorf("mcp: unknown transport %q", cfg.Transport)
	}
	if cfg.Name == "" {
		cfg.Name = "smart-agent-sdk-go"
	}
	if cfg.Version == "" {
		cfg.Version = moduleVersion()
	}
	if cfg.Transport == TransportHTTP {
		endpoint, err := url.Parse(cfg.URL)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil {
			return nil, errors.New("mcp: invalid HTTP endpoint")
		}
		if !validHTTPHeaders(cfg.HTTPHeaders) {
			return nil, errInvalidHTTPHeaders
		}
		for name, templates := range cfg.HTTPHeaders {
			if cfg.OAuthHandler != nil && strings.EqualFold(name, "Authorization") {
				return nil, errors.New("mcp: OAuthHandler conflicts with Authorization header")
			}
			for _, template := range templates {
				_, dynamic, err := renderHeaderTemplate(template, nil)
				if err != nil {
					return nil, fmt.Errorf("%w: header %q: unknown template variable", ErrHeaderTemplate, name)
				}
				if dynamic && cfg.HeaderProvider == nil {
					return nil, fmt.Errorf("%w: header %q uses a template variable but Config.HeaderProvider is nil", ErrHeaderTemplate, name)
				}
			}
		}
		cfg.HTTPHeaders = cfg.HTTPHeaders.Clone()
	}
	return &Client{cfg: cfg}, nil
}

// Connect establishes the transport and performs the MCP initialize
// handshake. It is idempotent: subsequent calls return nil.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectLocked(ctx)
}

// connectLocked establishes a new session. c.mu must be held.
func (c *Client) connectLocked(ctx context.Context) error {
	if c.session != nil {
		return nil
	}
	if c.closed {
		return errors.New("mcp: client closed")
	}

	var transport mcpsdk.Transport
	switch c.cfg.Transport {
	case TransportStdio:
		// procCancel guarantees the child process dies on Close even if the
		// SDK's stdin-close/SIGTERM path stalls.
		procCtx, cancel := context.WithCancel(context.Background())
		c.procCancel = cancel
		transport = &mcpsdk.CommandTransport{Command: exec.CommandContext(procCtx, c.cfg.Command, c.cfg.Args...)}
	case TransportHTTP:
		transport = &mcpsdk.StreamableClientTransport{
			Endpoint:             c.cfg.URL,
			HTTPClient:           c.httpClient(),
			DisableStandaloneSSE: c.cfg.DisableStandaloneSSE,
			OAuthHandler:         c.cfg.OAuthHandler,
		}
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: c.cfg.Name, Version: c.cfg.Version}, nil)
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		if c.procCancel != nil {
			c.procCancel()
			c.procCancel = nil
		}
		return fmt.Errorf("mcp: connect via %s: %w", c.cfg.Transport, err)
	}
	c.session = sess
	return nil
}

// httpClient returns a copy of the configured client whose transport decorates
// only this MCP client's requests. Neither the caller's client nor the request
// passed to the configured transport is mutated.
func (c *Client) httpClient() *http.Client {
	source := c.cfg.HTTPClient
	if source == nil {
		source = http.DefaultClient
	}
	client := *source
	endpoint, _ := url.Parse(c.cfg.URL)
	client.Transport = &headerTransport{
		base:           source.Transport,
		endpoint:       endpoint,
		staticHeaders:  c.cfg.HTTPHeaders,
		headerProvider: c.cfg.HeaderProvider,
	}
	return &client
}

type headerTransport struct {
	base           http.RoundTripper
	endpoint       *url.URL
	staticHeaders  http.Header
	headerProvider HeaderProvider
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errInvalidHTTPHeaders
	}
	// Redirects must not replay dynamically injected credentials to another origin.
	if t.endpoint != nil && (req.URL.Scheme != t.endpoint.Scheme || !strings.EqualFold(req.URL.Host, t.endpoint.Host)) {
		return nil, errors.New("mcp: cross-origin HTTP request refused")
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	var values HeaderValues
	if t.headerProvider != nil {
		var err error
		values, err = t.headerProvider(req.Context())
		if err != nil {
			return nil, ErrHeaderProvider
		}
	}
	headers := t.staticHeaders.Clone()
	for name, templates := range headers {
		for i, template := range templates {
			value, _, err := renderHeaderTemplate(template, &values)
			if err != nil || !validHTTPHeaderValue(value) {
				return nil, ErrHeaderProvider
			}
			headers[name][i] = value
		}
	}
	request := req.Clone(req.Context())
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	if !applyHTTPHeaders(request.Header, headers) {
		return nil, errInvalidHTTPHeaders
	}
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(request)
}

func renderHeaderTemplate(template string, values *HeaderValues) (string, bool, error) {
	var out strings.Builder
	dynamic := false
	for i := 0; i < len(template); {
		c := template[i]
		atVariable := c == '{' || c == '}' || (c == '$' && i+1 < len(template) && template[i+1] == '{')
		if !atVariable {
			out.WriteByte(c)
			i++
			continue
		}
		var variable, value string
		switch {
		case strings.HasPrefix(template[i:], "{username}"):
			variable = "{username}"
			if values != nil {
				value = values.Username
			}
		case strings.HasPrefix(template[i:], "{group}"):
			variable = "{group}"
			if values != nil {
				value = values.Group
			}
		case strings.HasPrefix(template[i:], "${token}"):
			variable = "${token}"
			if values != nil {
				value = values.Token
			}
		default:
			return "", false, ErrHeaderTemplate
		}
		if values != nil && value == "" {
			return "", true, ErrHeaderProvider
		}
		dynamic = true
		out.WriteString(value)
		i += len(variable)
	}
	return out.String(), dynamic, nil
}

// applyHTTPHeaders replaces values for each source header in dst. It copies
// each slice so neither static nor provider-owned maps can be modified by a
// request or its transport.
func applyHTTPHeaders(dst, source http.Header) bool {
	if !validHTTPHeaders(source) {
		return false
	}
	for name, values := range source {
		for existing := range dst {
			if strings.EqualFold(existing, name) {
				delete(dst, existing)
			}
		}
		if len(values) == 0 {
			continue
		}
		key := http.CanonicalHeaderKey(name)
		dst[key] = append([]string(nil), values...)
	}
	return true
}

func validHTTPHeaders(headers http.Header) bool {
	for name, values := range headers {
		if !validHTTPHeaderName(name) {
			return false
		}
		for _, value := range values {
			if !validHTTPHeaderValue(value) {
				return false
			}
		}
	}
	return true
}

func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func validHTTPHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < ' ' && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// Reconnect closes the current transport, if any, and establishes a new MCP
// session. It is useful after a remote disconnect or when a new session must
// use refreshed credentials. Like Connect, it cannot be used after Close.
// Tools returned before Reconnect belong to the old session; call Tools again
// after a successful reconnect.
func (c *Client) Reconnect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("mcp: client closed")
	}

	sess, procCancel := c.session, c.procCancel
	c.session = nil
	c.procCancel = nil
	if sess != nil {
		if err := sess.Close(); err != nil {
			if procCancel != nil {
				procCancel()
			}
			return fmt.Errorf("mcp: close before reconnect: %w", err)
		}
	}
	if procCancel != nil {
		procCancel()
	}
	return c.connectLocked(ctx)
}

// Tools lists the server's tools, adapted to the SDK's tool.Tool interface.
// The list is a snapshot; call again to observe tool list changes.
func (c *Client) Tools(ctx context.Context) ([]tool.Tool, error) {
	c.mu.Lock()
	sess := c.session
	c.mu.Unlock()
	if sess == nil {
		return nil, ErrNotConnected
	}

	var tools []tool.Tool
	params := &mcpsdk.ListToolsParams{}
	for {
		res, err := sess.ListTools(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("mcp: list tools: %w", err)
		}
		for _, t := range res.Tools {
			rt := &remoteTool{
				session:     sess,
				name:        t.Name,
				description: t.Description,
				schema:      schemaJSON(t.InputSchema),
			}
			if c.cfg.Policy != nil {
				tools = append(tools, tool.WithPolicy(rt, c.cfg.Policy))
			} else {
				tools = append(tools, rt)
			}
		}
		if res.NextCursor == "" {
			break
		}
		params.Cursor = res.NextCursor
	}
	return tools, nil
}

// Close releases transport resources and terminates a stdio child process.
// It is idempotent. Tool values previously returned by Tools become
// unusable and their Run calls fail with a connection-closed error.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	var err error
	if c.session != nil {
		err = c.session.Close()
	}
	if c.procCancel != nil {
		c.procCancel()
	}
	return err
}
