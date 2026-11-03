package server

import (
	"agent-core/internal/agent"
	"agent-core/internal/compressor"
	"agent-core/internal/config"
	"agent-core/internal/llm"
	"agent-core/internal/mcp"
	"agent-core/internal/memory"
	"agent-core/internal/model"
	"agent-core/internal/prompt"
	"agent-core/internal/store"
	"agent-core/internal/tool"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type serverTestLLM struct{}

func (serverTestLLM) Complete(context.Context, []model.Message, []model.ToolDefinition) (*model.Message, *model.UsageInfo, error) {
	return &model.Message{Role: model.RoleAssistant, Content: "title"}, &model.UsageInfo{}, nil
}
func (serverTestLLM) CompleteStream(context.Context, []model.Message, []model.ToolDefinition) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 2)
	ch <- llm.StreamChunk{Content: "hello"}
	ch <- llm.StreamChunk{Usage: &llm.StreamUsage{TotalTokens: 1}}
	close(ch)
	return ch, nil
}
func (serverTestLLM) Name() string          { return "test" }
func (serverTestLLM) ModelName() string     { return "test" }
func (serverTestLLM) MaxContextWindow() int { return 100000 }

func newServerHarness(t *testing.T) (*Server, *agent.Orchestrator) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Workspace.Path = t.TempDir()
	cfg.Tools.WorkDir = cfg.Workspace.Path
	cfg.Server.AuthToken = "secret"
	cfg.Server.MaxRequestBytes = 1024
	cfg.Server.CORSOrigins = []string{"https://app.example"}
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	provider := serverTestLLM{}
	registry := tool.NewRegistry()
	manager := mcp.NewManager(registry)
	memories := memory.NewService(db, nil, nil, cfg.Memory)
	orchestrator := agent.NewOrchestrator(provider, registry, tool.NewProcessRegistry(), db, memories,
		compressor.NewCompressor(cfg.Compression, nil), prompt.NewBuilder(), cfg)
	orchestrator.SetCheckpointStore(agent.NewSQLiteCheckpointStore(db))
	orchestrator.SetPermissionManager(tool.NewPermissionManager("yolo"))
	t.Cleanup(func() { orchestrator.StopAll(); manager.DisconnectAll(); _ = db.Close() })
	return NewServer(cfg, orchestrator, registry, manager, db, memories), orchestrator
}

func request(t *testing.T, server *Server, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	return response
}

func TestAuthAndHealth(t *testing.T) {
	server, _ := newServerHarness(t)
	if response := request(t, server, http.MethodGet, "/health", "", ""); response.Code != 200 {
		t.Fatalf("health=%d", response.Code)
	}
	if response := request(t, server, http.MethodGet, "/v1/conversations", "", ""); response.Code != 401 {
		t.Fatalf("missing auth=%d", response.Code)
	}
	if response := request(t, server, http.MethodGet, "/v1/conversations", "", "secret"); response.Code != 401 {
		t.Fatalf("raw token was accepted=%d", response.Code)
	}
	if response := request(t, server, http.MethodGet, "/v1/conversations", "", "Bearer secret"); response.Code != 200 {
		t.Fatalf("valid auth=%d body=%s", response.Code, response.Body.String())
	}
}

func TestStrictJSONAndBodyLimit(t *testing.T) {
	server, _ := newServerHarness(t)
	for _, body := range []string{`{"message":"x","unknown":true}`, `{"message":"x"} {}`} {
		if response := request(t, server, http.MethodPost, "/v1/chat", body, "Bearer secret"); response.Code != 400 {
			t.Fatalf("invalid JSON accepted: %d %s", response.Code, response.Body.String())
		}
	}
	large := `{"message":"` + strings.Repeat("x", 2000) + `"}`
	if response := request(t, server, http.MethodPost, "/v1/chat", large, "Bearer secret"); response.Code != 400 {
		t.Fatalf("oversized body=%d", response.Code)
	}
}

func TestCORSAndReadiness(t *testing.T) {
	server, _ := newServerHarness(t)
	req := httptest.NewRequest(http.MethodOptions, "/v1/chat", nil)
	req.Header.Set("Origin", "https://app.example")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != 204 || response.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatalf("CORS preflight=%d headers=%v", response.Code, response.Header())
	}
	req = httptest.NewRequest(http.MethodOptions, "/v1/chat", nil)
	req.Header.Set("Origin", "https://evil.example")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != 403 {
		t.Fatalf("disallowed origin=%d", response.Code)
	}
	if response := request(t, server, http.MethodGet, "/ready", "", "Bearer secret"); response.Code != 200 {
		t.Fatalf("readiness=%d %s", response.Code, response.Body.String())
	}
}

func TestSSEFramesIncludeIDsAndTerminalDone(t *testing.T) {
	server, _ := newServerHarness(t)
	response := request(t, server, http.MethodPost, "/v1/chat/stream", `{"message":"hello"}`, "Bearer secret")
	body := response.Body.String()
	for _, expected := range []string{"id: ", "event: chunk", "event: done", `"schema_version":1`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("SSE missing %q:\n%s", expected, body)
		}
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("request ID missing")
	}
}

func TestRateLimit(t *testing.T) {
	server, _ := newServerHarness(t)
	// The middleware instance captures its own limiter, so use a fresh server with a tiny limit.
	server.cfg.Server.RateLimitPerMinute = 2
	for i := 0; i < 2; i++ {
		if response := request(t, server, http.MethodGet, "/v1/conversations", "", "Bearer secret"); response.Code != 200 {
			t.Fatal(response.Code)
		}
	}
	if response := request(t, server, http.MethodGet, "/v1/conversations", "", "Bearer secret"); response.Code != 429 {
		t.Fatalf("rate limit=%d", response.Code)
	}
}

func TestRequestRejectsWrongJSONType(t *testing.T) {
	server, _ := newServerHarness(t)
	response := request(t, server, http.MethodPost, "/v1/chat", `{"message":42}`, "Bearer secret")
	if response.Code != 400 || !bytes.Contains(response.Body.Bytes(), []byte("bad_request")) {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}
