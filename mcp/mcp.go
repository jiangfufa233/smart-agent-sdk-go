// Package mcp integrates Model Context Protocol servers as tool sources.
//
// MVP scaffold: the public surface is defined here; transport implementations
// (stdio, streamable-http) will be built on top of
// github.com/modelcontextprotocol/go-sdk in a follow-up.
package mcp

import (
	"context"
	"errors"

	"github.com/jiangfufa233/openai-agent-sdk-go/tool"
)

// ErrNotImplemented marks functionality pending in the MVP scaffold.
var ErrNotImplemented = errors.New("mcp: not implemented in MVP scaffold")

// Transport selects how the MCP server is reached.
type Transport string

const (
	// TransportStdio launches the server as a child process.
	TransportStdio Transport = "stdio"
	// TransportHTTP connects to a streamable-http endpoint.
	TransportHTTP Transport = "streamable-http"
)

// Config describes how to reach one MCP server.
type Config struct {
	Transport Transport
	// Command/Args are used with TransportStdio.
	Command string
	Args    []string
	// URL is used with TransportHTTP.
	URL string
}

// Client is a connection to a single MCP server.
type Client struct {
	cfg Config
}

// NewClient creates a client for the server described by cfg.
func NewClient(cfg Config) (*Client, error) {
	return &Client{cfg: cfg}, nil
}

// Connect establishes the transport and performs the MCP handshake.
func (c *Client) Connect(ctx context.Context) error {
	return ErrNotImplemented
}

// Tools lists the server's tools adapted to the SDK's tool.Tool interface.
func (c *Client) Tools(ctx context.Context) ([]tool.Tool, error) {
	return nil, ErrNotImplemented
}

// Close releases transport resources.
func (c *Client) Close() error {
	return nil
}
