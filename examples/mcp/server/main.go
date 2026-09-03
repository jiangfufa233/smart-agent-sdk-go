// Command server is the example MCP server used by examples/mcp. It exposes
// a typed "add" tool with structured output and a text "shout" tool, and
// speaks newline-delimited JSON over stdin/stdout.
package main

import (
	"context"
	"log"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

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

func main() {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "example-mcp-server", Version: "1.0.0"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "add", Description: "Add two integers"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in addIn) (*mcpsdk.CallToolResult, addOut, error) {
			return nil, addOut{Sum: in.A + in.B}, nil
		})
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "shout", Description: "Repeat text in capitals"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in shoutIn) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: strings.ToUpper(in.Text)}}}, nil, nil
		})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
