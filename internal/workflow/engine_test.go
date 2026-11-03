package workflow

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"agent-core/internal/store"
	"agent-core/internal/tool"
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
)

type fakeStarter struct{ calls atomic.Int32 }

func (f *fakeStarter) StartChildRun(_ context.Context, req model.ChatRequest, spec model.RunSpec, runID string, _ bool, _ chan<- model.StreamEvent) (*model.ChatResponse, error) {
	f.calls.Add(1)
	return &model.ChatResponse{RunID: runID, Status: model.RunStatusCompleted, Reply: "done: " + req.Message}, nil
}
func (f *fakeStarter) StopRun(string) bool { return true }

type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "echo" }
func (echoTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Properties: map[string]any{}}
}
func (echoTool) Execute(context.Context, map[string]any) (string, error) { return "ok", nil }
func (echoTool) Capabilities() tool.Capabilities                         { return tool.Capabilities{ReadOnly: true} }

func TestWorkflowFanoutAndResumeUsesCache(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	starter := &fakeStarter{}
	registry := tool.NewRegistry()
	if err := registry.Register(echoTool{}, tool.SourceBuiltin, "builtin"); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	engine := NewEngine(db, starter, registry, cfg)
	definition := Definition{SchemaVersion: 1, Name: "test", Nodes: []Node{
		{ID: "review", Type: "agent", Prompt: "review {{item}}", Items: []any{"a", "b"}},
		{ID: "join", Type: "tool", Tool: "echo", DependsOn: []string{"review"}},
	}}
	view, err := engine.Start(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}
	if view.Record.Status != "completed" || starter.calls.Load() != 2 || len(view.Nodes) != 3 {
		t.Fatalf("unexpected run: status=%s calls=%d nodes=%d", view.Record.Status, starter.calls.Load(), len(view.Nodes))
	}
	if _, err := engine.Resume(context.Background(), view.Record.ID); err != nil {
		t.Fatal(err)
	}
	if starter.calls.Load() != 2 {
		t.Fatalf("resume repeated completed agent nodes: %d", starter.calls.Load())
	}
}

func TestDefinitionRejectsCycle(t *testing.T) {
	definition := Definition{SchemaVersion: 1, Name: "cycle", Nodes: []Node{
		{ID: "a", Type: "agent", Prompt: "a", DependsOn: []string{"b"}},
		{ID: "b", Type: "agent", Prompt: "b", DependsOn: []string{"a"}},
	}}
	if err := definition.Validate(10); err == nil {
		t.Fatal("expected cycle error")
	}
}
