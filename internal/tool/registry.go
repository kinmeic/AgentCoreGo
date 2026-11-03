package tool

import (
	"context"
	"encoding/json"
)

// ToolSchema represents the JSON Schema for a tool's parameters.
type ToolSchema struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Required   []string       `json:"required,omitempty"`
}

// Provider defines the interface that all tool providers must implement.
type Provider interface {
	// Name returns the unique identifier for this tool.
	Name() string

	// Description returns a human-readable description of what the tool does.
	Description() string

	// Schema returns the JSON Schema for the tool's parameters.
	Schema() ToolSchema

	// Execute runs the tool with the given arguments.
	Execute(ctx context.Context, args map[string]any) (string, error)
}

// Source indicates where a tool comes from.
type Source string

const (
	SourceBuiltin      Source = "builtin"
	SourceMCP          Source = "mcp"
	SourceHostCallback Source = "host_callback"
)

// RegisteredTool wraps a Provider with metadata.
type RegisteredTool struct {
	Provider     Provider
	Source       Source
	Origin       string // e.g., MCP server name or "builtin"
	Capabilities Capabilities
}

// MarshalArgs is a helper to serialize tool arguments to JSON string.
func MarshalArgs(args map[string]any) string {
	data, _ := json.Marshal(args)
	return string(data)
}

// UnmarshalArgs is a helper to deserialize tool arguments from JSON string.
func UnmarshalArgs(s string) (map[string]any, error) {
	var args map[string]any
	if s == "" {
		return args, nil
	}
	err := json.Unmarshal([]byte(s), &args)
	return args, err
}
