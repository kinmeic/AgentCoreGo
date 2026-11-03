package mcp

import (
	"agent-core/internal/tool"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStderrBufferSanitizesSecretAndANSI(t *testing.T) {
	buffer := &stderrBuffer{}
	_, _ = buffer.Write([]byte("\x1b[31mapi_key=secret-token\x1b[0m\n"))
	summary := buffer.summary()
	if strings.Contains(summary, "secret-token") || !strings.Contains(summary, "[redacted]") || strings.Contains(summary, "\x1b") {
		t.Fatalf("unexpected sanitized summary: %q", summary)
	}
}

func TestMCPStdioFailureIncludesSanitizedStderr(t *testing.T) {
	manager := NewManager(tool.NewRegistry())
	config, _ := json.Marshal(StdioConfig{Command: "/bin/sh", Args: []string{"-c", "printf 'api_key=secret-token\\n' >&2; exit 1"}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.Connect(ctx, ServerConfig{Name: "broken", Transport: "stdio", Config: config}); err == nil {
		t.Fatal("expected stdio connection failure")
	}
	servers := manager.ListServers()
	if len(servers) != 1 || servers[0].Status != "failed" || !strings.Contains(servers[0].Error, "[redacted]") {
		t.Fatalf("unexpected server status: %#v", servers)
	}
	if strings.Contains(servers[0].Error, "secret-token") {
		t.Fatal("stderr secret was not redacted")
	}
}
