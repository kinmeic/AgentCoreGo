package mcp

import (
	"agent-core/internal/tool"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func newTestServer() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "agent-core-test", Version: "1.0.0"}, nil)
	server.AddTool(&sdk.Tool{Name: "echo", Description: "echo a value", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"},
	}}, func(_ context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var args map[string]string
		if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "echo:" + args["value"]}}}, nil
	})
	return server
}

func TestMCPStdioHelper(t *testing.T) {
	if os.Getenv("AGENTCORE_MCP_HELPER") != "1" {
		return
	}
	if err := newTestServer().Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestManagerStdioConnectCallDisconnect(t *testing.T) {
	registry := tool.NewRegistry()
	manager := NewManager(registry)
	config, _ := json.Marshal(StdioConfig{Command: os.Args[0], Args: []string{"-test.run=TestMCPStdioHelper"},
		Env: map[string]string{"AGENTCORE_MCP_HELPER": "1"}})
	exerciseConnection(t, manager, registry, ServerConfig{Name: "stdio", Transport: "stdio", Config: config})
}

func TestManagerStreamableHTTP(t *testing.T) {
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return newTestServer() }, nil)
	server := httptest.NewServer(handler)
	defer server.Close()
	registry := tool.NewRegistry()
	manager := NewManager(registry)
	config, _ := json.Marshal(HTTPConfig{URL: server.URL, AllowPrivate: true})
	exerciseConnection(t, manager, registry, ServerConfig{Name: "http", Transport: "streamable_http", Config: config})
}

func TestManagerSSE(t *testing.T) {
	handler := sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return newTestServer() }, nil)
	server := httptest.NewServer(handler)
	defer server.Close()
	registry := tool.NewRegistry()
	manager := NewManager(registry)
	config, _ := json.Marshal(SSEConfig{URL: server.URL, AllowPrivate: true})
	exerciseConnection(t, manager, registry, ServerConfig{Name: "sse", Transport: "sse", Config: config})
}

func exerciseConnection(t *testing.T, manager *Manager, registry *tool.Registry, config ServerConfig) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := manager.Connect(ctx, config); err != nil {
		cancel()
		t.Fatalf("connect: %v", err)
	}
	cancel() // The initial HTTP/RPC deadline must not kill the established session.
	name := config.Name + "__echo"
	registered, ok := registry.Get(name)
	if !ok {
		t.Fatalf("tool %s was not discovered", name)
	}
	result, err := registry.Dispatch(context.Background(), name, map[string]any{"value": "works"})
	if err != nil || result != "echo:works" {
		t.Fatalf("call: result=%q err=%v", result, err)
	}
	if registered.Capabilities.RiskLevel != "high" {
		t.Fatal("untrusted MCP tool did not use conservative capabilities")
	}
	if err := manager.Disconnect(config.Name); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if _, ok := registry.Get(name); ok {
		t.Fatal("tool remained registered after disconnect")
	}
}

func TestPrivateMCPRequiresExplicitOptIn(t *testing.T) {
	server := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return newTestServer() }, nil))
	defer server.Close()
	manager := NewManager(tool.NewRegistry())
	config, _ := json.Marshal(HTTPConfig{URL: server.URL})
	err := manager.Connect(context.Background(), ServerConfig{Name: "private", Transport: "streamable_http", Config: config})
	if err == nil {
		t.Fatal("private endpoint connected without allow_private")
	}
}

func TestMCPHeadersAreForwarded(t *testing.T) {
	base := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return newTestServer() }, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			http.Error(w, "missing auth", http.StatusForbidden)
			return
		}
		base.ServeHTTP(w, r)
	}))
	defer server.Close()
	manager := NewManager(tool.NewRegistry())
	config, _ := json.Marshal(HTTPConfig{URL: server.URL, AllowPrivate: true, Headers: map[string]string{"Authorization": "Bearer test"}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := manager.Connect(ctx, ServerConfig{Name: "auth", Transport: "streamable_http", Config: config}); err != nil {
		t.Fatalf("authenticated connect: %v", err)
	}
	if err := manager.Disconnect("auth"); err != nil {
		t.Fatal(err)
	}
}
