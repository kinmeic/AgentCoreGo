package mcp

import (
	"agent-core/internal/tool"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type remoteTool struct {
	name        string
	description string
	remoteName  string
	schema      map[string]any
	session     *sdk.ClientSession
}

func newRemoteTool(serverName string, remote *sdk.Tool, session *sdk.ClientSession) *remoteTool {
	schema := map[string]any{"type": "object", "properties": map[string]any{}}
	if encoded, err := json.Marshal(remote.InputSchema); err == nil && string(encoded) != "null" {
		_ = json.Unmarshal(encoded, &schema)
	}
	return &remoteTool{name: namespaceToolName(serverName, remote.Name), description: remote.Description,
		remoteName: remote.Name, schema: schema, session: session}
}

func namespaceToolName(server, name string) string {
	value := server + "__" + name
	var result strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			result.WriteRune(r)
		} else {
			result.WriteRune('_')
		}
	}
	return result.String()
}

func (t *remoteTool) Name() string        { return t.name }
func (t *remoteTool) Description() string { return t.description }
func (t *remoteTool) JSONSchema() map[string]any {
	encoded, _ := json.Marshal(t.schema)
	var clone map[string]any
	_ = json.Unmarshal(encoded, &clone)
	return clone
}
func (t *remoteTool) Schema() tool.ToolSchema {
	schema := tool.ToolSchema{Type: "object", Properties: map[string]any{}}
	if value, ok := t.schema["type"].(string); ok {
		schema.Type = value
	}
	if value, ok := t.schema["properties"].(map[string]any); ok {
		schema.Properties = value
	}
	if values, ok := t.schema["required"].([]any); ok {
		for _, value := range values {
			if name, ok := value.(string); ok {
				schema.Required = append(schema.Required, name)
			}
		}
	}
	return schema
}
func (t *remoteTool) Capabilities() tool.Capabilities {
	// Server annotations are hints, not an authorization boundary.
	return tool.Capabilities{NeedsApproval: true, SideEffectScope: "external", RiskLevel: "high", Timeout: 2 * time.Minute, MaxOutputBytes: 100_000}
}
func (t *remoteTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	result, err := t.session.CallTool(ctx, &sdk.CallToolParams{Name: t.remoteName, Arguments: args})
	if err != nil {
		return "", err
	}
	var text strings.Builder
	for _, content := range result.Content {
		if value, ok := content.(*sdk.TextContent); ok {
			text.WriteString(value.Text)
		} else {
			encoded, err := json.Marshal(content)
			if err == nil {
				text.Write(encoded)
			}
		}
	}
	if result.StructuredContent != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		if err == nil {
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.Write(encoded)
		}
	}
	if text.Len() == 0 {
		text.WriteString("(empty MCP tool result)")
	}
	if result.IsError {
		return text.String(), errors.New(text.String())
	}
	return text.String(), nil
}

func (t *remoteTool) String() string { return fmt.Sprintf("MCP tool %s", t.name) }
