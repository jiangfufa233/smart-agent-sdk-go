package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jiangfufa233/smart-agent-sdk-go/model"
)

const modulePath = "github.com/jiangfufa233/smart-agent-sdk-go"

// remoteTool adapts one server-side MCP tool to the SDK's tool.Tool
// interface. Calls are serialized through the shared client session.
type remoteTool struct {
	session     *mcpsdk.ClientSession
	name        string
	description string
	schema      json.RawMessage
}

// Spec implements tool.Tool.
func (t *remoteTool) Spec() model.ToolParam {
	return model.ToolParam{
		Type: "function",
		Function: model.FunctionDef{
			Name:        t.name,
			Description: t.description,
			Parameters:  t.schema,
		},
	}
}

// Run implements tool.Tool. The MCP tool result is flattened to text:
// text content is passed through, binary content (images, audio, blobs) is
// replaced by a size placeholder, resources contribute their text or URI,
// and structured output is appended as JSON. A result with IsError set is
// reported as an error so the runner records it in RunResult.ToolErrors
// while still feeding the message back to the model.
func (t *remoteTool) Run(ctx context.Context, argumentsJSON string) (string, error) {
	if strings.TrimSpace(argumentsJSON) == "" {
		argumentsJSON = "{}"
	}
	res, err := t.session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      t.name,
		Arguments: json.RawMessage(argumentsJSON),
	})
	if err != nil {
		return "", fmt.Errorf("mcp: tool %q: %w", t.name, err)
	}
	out := flattenResult(res)
	if res.IsError {
		return "", fmt.Errorf("mcp: tool %q failed: %s", t.name, out)
	}
	return out, nil
}

// schemaJSON normalizes a server-provided input schema to a JSON document,
// falling back to a permissive empty object schema.
func schemaJSON(inputSchema any) json.RawMessage {
	const fallback = `{"type":"object"}`
	if inputSchema == nil {
		return json.RawMessage(fallback)
	}
	data, err := json.Marshal(inputSchema)
	if err != nil {
		return json.RawMessage(fallback)
	}
	return data
}

// flattenResult renders a CallToolResult as plain text for the model.
func flattenResult(res *mcpsdk.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		switch c := c.(type) {
		case *mcpsdk.TextContent:
			parts = append(parts, c.Text)
		case *mcpsdk.ImageContent:
			parts = append(parts, fmt.Sprintf("[image: %s, %d bytes omitted]", c.MIMEType, len(c.Data)))
		case *mcpsdk.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio: %s, %d bytes omitted]", c.MIMEType, len(c.Data)))
		case *mcpsdk.ResourceLink:
			parts = append(parts, fmt.Sprintf("[resource link: %s]", c.URI))
		case *mcpsdk.EmbeddedResource:
			if r := c.Resource; r != nil {
				if r.Text != "" {
					parts = append(parts, r.Text)
				} else {
					parts = append(parts, fmt.Sprintf("[resource: %s, %d bytes omitted]", r.URI, len(r.Blob)))
				}
			}
		default:
			parts = append(parts, fmt.Sprintf("[unsupported content: %T]", c))
		}
	}
	if res.StructuredContent != nil {
		if data, err := json.Marshal(res.StructuredContent); err == nil {
			js := string(data)
			// Structured output is usually mirrored as JSON text content by
			// the server; only append it when it adds information.
			if js != strings.TrimSpace(strings.Join(parts, "\n")) {
				parts = append(parts, js)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// moduleVersion reports the tagged version of this module from the build
// info of the consuming program, or "dev" when built from source.
func moduleVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if bi.Main.Path == modulePath && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	for _, dep := range bi.Deps {
		if dep.Path == modulePath && dep.Version != "" {
			return dep.Version
		}
	}
	return "dev"
}
