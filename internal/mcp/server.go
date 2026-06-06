package mcp

import (
	"agent-core/internal/tool"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

// Server exposes AgentCore's tools via the MCP (Model Context Protocol) interface.
// This enables AgentCore to act as an MCP server — other agents or tools can
// discover and call AgentCore's tools through the standard MCP protocol.
type Server struct {
	registry    *tool.Registry
	transport   string // "stdio" or "sse"
	initialized bool   // Track whether initialize has been called
}

// NewServer creates a new MCP server that exposes the given tool registry.
func NewServer(registry *tool.Registry, transport string) *Server {
	return &Server{
		registry:  registry,
		transport: transport,
	}
}

// Serve starts the MCP server on the configured transport.
func (s *Server) Serve() error {
	switch s.transport {
	case "stdio":
		return s.serveStdio()
	default:
		return fmt.Errorf("unsupported transport: %s", s.transport)
	}
}

// --- JSON-RPC types for MCP ---

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      any         `json:"id"`
	Result  any         `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// serveStdio implements MCP over stdin/stdout (JSON-RPC 2.0).
func (s *Server) serveStdio() error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	log.Printf("[mcp-server] Listening on stdio (MCP protocol)")

	for scanner.Scan() {
		line := scanner.Bytes()
		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(nil, -32700, "Parse error")
			continue
		}

		s.handleRequest(&req)
	}

	return scanner.Err()
}

func (s *Server) handleRequest(req *jsonRPCRequest) {
	// MCP spec requires initialize before other methods
	if req.Method != "initialize" && !s.initialized {
		s.writeError(req.ID, -32002, "Server not initialized")
		return
	}

	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "tools/list":
		s.handleToolsList(req)
	case "tools/call":
		s.handleToolsCall(req)
	default:
		s.writeError(req.ID, -32601, fmt.Sprintf("Method not found: %s", req.Method))
	}
}

func (s *Server) handleInitialize(req *jsonRPCRequest) {
	s.initialized = true
	s.writeResult(req.ID, map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]string{
			"name":    "agent-core",
			"version": "1.0.0",
		},
	})
}

func (s *Server) handleToolsList(req *jsonRPCRequest) {
	tools := s.registry.List()
	mcpTools := make([]map[string]any, 0, len(tools))

	for _, rt := range tools {
		// Skip bridge tools (they're internal)
		name := rt.Provider.Name()
		if name == "tool_search" || name == "tool_describe" {
			continue
		}

		schema := rt.Provider.Schema()
		mcpTools = append(mcpTools, map[string]any{
			"name":        name,
			"description": rt.Provider.Description(),
			"inputSchema": map[string]any{
				"type":       schema.Type,
				"properties": schema.Properties,
				"required":   schema.Required,
			},
		})
	}

	s.writeResult(req.ID, map[string]any{
		"tools": mcpTools,
	})
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) handleToolsCall(req *jsonRPCRequest) {
	var params toolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeError(req.ID, -32602, "Invalid params")
		return
	}

	// Create a context with timeout for tool execution
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := s.registry.Dispatch(ctx, params.Name, params.Arguments)
	if err != nil {
		s.writeResult(req.ID, map[string]any{
			"content": []map[string]any{
				{
					"type": "text",
					"text": fmt.Sprintf("Error: %v", err),
				},
			},
			"isError": true,
		})
		return
	}

	s.writeResult(req.ID, map[string]any{
		"content": []map[string]any{
			{
				"type": "text",
				"text": result,
			},
		},
		"isError": false,
	})
}

func (s *Server) writeResult(id any, result any) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	data, _ := json.Marshal(resp)
	fmt.Println(string(data))
}

func (s *Server) writeError(id any, code int, message string) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	}
	data, _ := json.Marshal(resp)
	fmt.Println(string(data))
}
