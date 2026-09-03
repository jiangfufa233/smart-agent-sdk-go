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
	"os/exec"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jiangfufa233/smart-agent-sdk-go/tool"
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
	return &Client{cfg: cfg}, nil
}

// Connect establishes the transport and performs the MCP initialize
// handshake. It is idempotent: subsequent calls return nil.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
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
			HTTPClient:           c.cfg.HTTPClient,
			DisableStandaloneSSE: c.cfg.DisableStandaloneSSE,
		}
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: c.cfg.Name, Version: c.cfg.Version}, nil)
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		if c.procCancel != nil {
			c.procCancel()
		}
		return fmt.Errorf("mcp: connect via %s: %w", c.cfg.Transport, err)
	}
	c.session = sess
	return nil
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
