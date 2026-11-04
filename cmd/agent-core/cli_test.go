package main

import (
	"agent-core/internal/agent"
	"agent-core/internal/compressor"
	"agent-core/internal/config"
	"agent-core/internal/llm"
	"agent-core/internal/mcp"
	"agent-core/internal/memory"
	"agent-core/internal/model"
	"agent-core/internal/prompt"
	"agent-core/internal/server"
	"agent-core/internal/store"
	"agent-core/internal/tool"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type cliTestLLM struct{ calls atomic.Int32 }

func (p *cliTestLLM) Complete(context.Context, []model.Message, []model.ToolDefinition) (*model.Message, *model.UsageInfo, error) {
	return &model.Message{Role: model.RoleAssistant, Content: "title"}, &model.UsageInfo{}, nil
}
func (p *cliTestLLM) CompleteStream(context.Context, []model.Message, []model.ToolDefinition) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 4)
	if p.calls.Add(1) == 1 {
		ch <- llm.StreamChunk{ToolCalls: []llm.ToolCallDelta{{Index: 0, ID: "call", Type: "function", Name: "risky", Arguments: "{}"}}}
	} else {
		ch <- llm.StreamChunk{Content: "approved done"}
	}
	ch <- llm.StreamChunk{Usage: &llm.StreamUsage{TotalTokens: 1}}
	close(ch)
	return ch, nil
}
func (p *cliTestLLM) Name() string          { return "test" }
func (p *cliTestLLM) ModelName() string     { return "test" }
func (p *cliTestLLM) MaxContextWindow() int { return 100000 }

type cliRiskyTool struct{ calls atomic.Int32 }

func (t *cliRiskyTool) Name() string        { return "risky" }
func (t *cliRiskyTool) Description() string { return "test approval" }
func (t *cliRiskyTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Properties: map[string]any{}}
}
func (t *cliRiskyTool) Execute(context.Context, map[string]any) (string, error) {
	t.calls.Add(1)
	return "ok", nil
}
func (t *cliRiskyTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{NeedsApproval: true, SideEffectScope: "workspace", RiskLevel: "medium"}
}

func TestCLIChatApprovalEndToEnd(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Workspace.Path = t.TempDir()
	cfg.Tools.WorkDir = cfg.Workspace.Path
	cfg.Server.AuthToken = "secret"
	cfg.Tools.ApprovalMode = "build"
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "cli.db"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &cliTestLLM{}
	registry := tool.NewRegistry()
	risky := &cliRiskyTool{}
	if err := registry.Register(risky, tool.SourceBuiltin, "builtin"); err != nil {
		t.Fatal(err)
	}
	permissions := tool.NewPermissionManager("build")
	registry.SetPolicyCheck(func(ctx context.Context, name string, _ map[string]any) error {
		registered, _ := registry.Get(name)
		return permissions.Authorize(ctx, registered, nil)
	})
	memories := memory.NewService(db, cfg.Memory)
	orchestrator := agent.NewOrchestrator(provider, registry, tool.NewProcessRegistry(), db, memories,
		compressor.NewCompressor(cfg.Compression, nil), prompt.NewBuilder(), cfg)
	orchestrator.SetCheckpointStore(agent.NewSQLiteCheckpointStore(db))
	orchestrator.SetPermissionManager(permissions)
	manager := mcp.NewManager(registry)
	api := server.NewServer(cfg, orchestrator, registry, manager, db, memories)
	httpServer := httptest.NewServer(api.Handler())
	defer httpServer.Close()
	t.Cleanup(func() { orchestrator.StopAll(); manager.DisconnectAll(); _ = db.Close() })

	stdinReader, stdinWriter, _ := os.Pipe()
	_, _ = stdinWriter.WriteString("y\n")
	_ = stdinWriter.Close()
	stdoutReader, stdoutWriter, _ := os.Pipe()
	stderrReader, stderrWriter, _ := os.Pipe()
	oldStdin, oldStdout, oldStderr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = stdinReader, stdoutWriter, stderrWriter
	defer func() { os.Stdin, os.Stdout, os.Stderr = oldStdin, oldStdout, oldStderr }()

	err = cliChat([]string{"--url", httpServer.URL, "--token", "secret", "--message", "run tool"})
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	stdout, _ := io.ReadAll(stdoutReader)
	stderr, _ := io.ReadAll(stderrReader)
	if err != nil {
		t.Fatalf("cli chat: %v; stderr=%s", err, stderr)
	}
	if risky.calls.Load() != 1 {
		t.Fatalf("approved tool calls=%d", risky.calls.Load())
	}
	if !strings.Contains(string(stdout), "approved done") || !strings.Contains(string(stderr), "status=completed") {
		t.Fatalf("unexpected output stdout=%q stderr=%q", stdout, stderr)
	}
}
