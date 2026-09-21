// Command mcp demonstrates using MCP servers as tool sources in the agent
// loop: a stdio MCP server is launched as a child process, its tools are
// adapted to tool.Tool, and a scripted model drives two tool calls — one
// denied by the policy, one executed remotely over MCP. A second section
// connects to a streamable-http MCP server with header template
// authentication ({username}, ${token}).
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jiangfufa233/smart-agent-sdk-go/v2/agent"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/mcp"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/model"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/testutil"
	"github.com/jiangfufa233/smart-agent-sdk-go/v2/tool"
)

func main() {
	ctx := context.Background()

	// Launch the example MCP server as a child process over stdio, and deny
	// the "shout" tool via policy to show authorization inside the loop.
	client, err := mcp.NewClient(mcp.Config{
		Transport: mcp.TransportStdio,
		Command:   "go",
		Args:      []string{"run", "./examples/mcp/server"},
		Policy:    tool.Denylist("shout"),
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	tools, err := client.Tools(ctx)
	if err != nil {
		log.Fatal(err)
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Spec().Function.Name)
	}
	fmt.Printf("== MCP tools from the server: %v (policy denies \"shout\") ==\n", names)

	// A scripted model stands in for a real LLM. Each step asserts what the
	// agent fed back before choosing the next action.
	m := testutil.NewScripted(
		testutil.ToolCallStep("c1", "shout", `{"text":"hello"}`),
		testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
			if got := lastToolContent(req); !strings.Contains(got, "denied by policy") {
				return nil, fmt.Errorf("expected denial fed back to the model, got %q", got)
			}
			return testutil.ToolCallStep("c2", "add", `{"a":40,"b":2}`).Resp, nil
		}},
		testutil.Step{Func: func(req *model.Request) (*model.Response, error) {
			if got := lastToolContent(req); !strings.Contains(got, `"sum":42`) {
				return nil, fmt.Errorf("expected MCP result fed back to the model, got %q", got)
			}
			return testutil.TextStep("40 + 2 = 42").Resp, nil
		}},
	)

	a := &agent.Agent{ID: "mcp-demo", Name: "mcp-demo", Instructions: "Use the available MCP tools.", Model: m, Tools: tools}
	res, err := agent.NewRunner().Run(ctx, a, "please add 40 and 2")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("== tool errors recorded (the denial): %d ==\n", len(res.ToolErrors))
	fmt.Printf("== final output: %s ==\n", res.Output)

	demoTemplateAuth(ctx)
}

type authEcho struct {
	Authorization string `json:"authorization"`
	ActingUser    string `json:"acting_user"`
}

// demoTemplateAuth serves an MCP server over streamable-http on localhost and
// connects with header templates: every request renders "Bearer ${token}" and
// "{username}" from the values supplied by HeaderProvider.
func demoTemplateAuth(ctx context.Context) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "auth-demo-server", Version: "1.0.0"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "echo_auth", Description: "Echo the request credentials"},
		func(_ context.Context, req *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, authEcho, error) {
			extra := req.GetExtra()
			return nil, authEcho{
				Authorization: extra.Header.Get("Authorization"),
				ActingUser:    extra.Header.Get("X-Acting-User"),
			}, nil
		})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	httpServer := &http.Server{Handler: mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = httpServer.Close() }()

	client, err := mcp.NewClient(mcp.Config{
		Transport:            mcp.TransportHTTP,
		URL:                  "http://" + listener.Addr().String() + "/mcp",
		DisableStandaloneSSE: true,
		HTTPHeaders: http.Header{
			"Authorization": {"Bearer ${token}"},
			"X-Acting-User": {"{username}"},
		},
		HeaderProvider: func(context.Context) (mcp.HeaderValues, error) {
			// In production this would refresh a credential; here it is static.
			return mcp.HeaderValues{Username: "alice", Token: "demo-secret"}, nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	tools, err := client.Tools(ctx)
	if err != nil {
		log.Fatal(err)
	}
	out, err := tools[0].Run(ctx, "{}")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("== template auth over streamable-http, server saw: %s ==\n", strings.TrimSpace(out))
}

// lastToolContent returns the content of the most recent tool-role message.
func lastToolContent(req *model.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == model.RoleTool {
			return req.Messages[i].Content
		}
	}
	return ""
}
