package mcp

import (
	"agent-core/internal/tool"
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server exposes AgentCore tools through the official MCP implementation.
type Server struct {
	registry  *tool.Registry
	transport string
}

func NewServer(registry *tool.Registry, transport string) *Server {
	return &Server{registry: registry, transport: transport}
}

func (s *Server) Serve() error { return s.ServeContext(context.Background()) }

func (s *Server) ServeContext(ctx context.Context) error {
	if s.transport != "stdio" {
		return fmt.Errorf("unsupported transport: %s", s.transport)
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "agent-core", Version: "0.3.1"}, nil)
	for _, registered := range s.registry.List() {
		name := registered.Provider.Name()
		if name == "tool_search" || name == "tool_describe" || !s.registry.IsToolEnabled(name) {
			continue
		}
		server.AddTool(&sdk.Tool{Name: name, Description: registered.Provider.Description(),
			InputSchema: publicSchema(registered.Provider)}, func(ctx context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			var args map[string]any
			if len(request.Params.Arguments) > 0 {
				if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
					return nil, fmt.Errorf("decode tool arguments: %w", err)
				}
			}
			result, err := s.registry.Dispatch(ctx, request.Params.Name, args)
			if err != nil {
				return &sdk.CallToolResult{IsError: true,
					Content: []sdk.Content{&sdk.TextContent{Text: "Error: " + err.Error()}}}, nil
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: result}}}, nil
		})
	}
	return server.Run(ctx, &sdk.StdioTransport{})
}

func publicSchema(provider tool.Provider) map[string]any {
	if raw, ok := provider.(tool.RawSchemaProvider); ok {
		return raw.JSONSchema()
	}
	schema := provider.Schema()
	result := map[string]any{"type": schema.Type, "properties": schema.Properties}
	if len(schema.Required) > 0 {
		result["required"] = schema.Required
	}
	return result
}
