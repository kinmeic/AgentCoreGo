package workflow

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"agent-core/internal/store"
	"agent-core/internal/tool"
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type fakeStarter struct {
	calls atomic.Int32
	fail  bool
}

func (f *fakeStarter) StartChildRun(_ context.Context, req model.ChatRequest, spec model.RunSpec, runID string, _ bool, _ chan<- model.StreamEvent) (*model.ChatResponse, error) {
	f.calls.Add(1)
	if f.fail {
		return nil, fmt.Errorf("agent failed")
	}
	return &model.ChatResponse{RunID: runID, Status: model.RunStatusCompleted, Reply: "done: " + req.Message}, nil
}

type retryTool struct {
	calls    atomic.Int32
	failures int32
}

func (*retryTool) Name() string        { return "retry" }
func (*retryTool) Description() string { return "retry test" }
func (*retryTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Properties: map[string]any{}}
}
func (r *retryTool) Execute(ctx context.Context, _ map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r.calls.Add(1) <= r.failures {
		return "", fmt.Errorf("transient failure")
	}
	return "recovered", nil
}

func TestWorkflowRetryIsBoundedAndRecovers(t *testing.T) {
	for _, failures := range []int32{1, 1000} {
		t.Run(fmt.Sprint(failures), func(t *testing.T) {
			db, err := store.NewSQLite(filepath.Join(t.TempDir(), "workflow.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			registry := tool.NewRegistry()
			provider := &retryTool{failures: failures}
			if err := registry.Register(provider, tool.SourceBuiltin, "builtin"); err != nil {
				t.Fatal(err)
			}
			engine := NewEngine(db, &fakeStarter{}, registry, config.DefaultConfig())
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			view, err := engine.Start(ctx, Definition{SchemaVersion: 1, Name: "retry", Nodes: []Node{{ID: "retry", Type: "tool", Tool: "retry", Retry: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			want := "completed"
			if failures > 1 {
				want = "failed"
			}
			if view.Record.Status != want || provider.calls.Load() != 2 || len(view.Nodes) != 2 {
				t.Fatalf("status=%s calls=%d attempts=%d; want %s, 2, 2", view.Record.Status, provider.calls.Load(), len(view.Nodes), want)
			}
			root, err := db.GetRun(view.Record.ID)
			if err != nil || root == nil || string(root.Status) != want || root.FinishedAt == nil {
				t.Fatalf("root run did not finish: %#v, %v", root, err)
			}
		})
	}
}

type requiredArgsTool struct{ received chan string }

func (*requiredArgsTool) Name() string        { return "required_args" }
func (*requiredArgsTool) Description() string { return "required arguments test" }
func (*requiredArgsTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"text"}, Properties: map[string]any{"text": map[string]any{"type": "string"}}}
}
func (*requiredArgsTool) JSONSchema() map[string]any {
	return map[string]any{"type": "object", "required": []string{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}}, "additionalProperties": false}
}
func (r *requiredArgsTool) Execute(_ context.Context, args map[string]any) (string, error) {
	text := args["text"].(string)
	r.received <- text
	return text, nil
}

func TestWorkflowPassesArgumentsAndExpandsTemplates(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry := tool.NewRegistry()
	provider := &requiredArgsTool{received: make(chan string, 4)}
	if err := registry.Register(provider, tool.SourceBuiltin, "builtin"); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(db, &fakeStarter{}, registry, config.DefaultConfig())
	view, err := engine.Start(context.Background(), Definition{SchemaVersion: 1, Name: "arguments", Nodes: []Node{
		{ID: "first", Type: "tool", Tool: provider.Name(), Args: map[string]any{"text": "hello"}},
		{ID: "fanout", Type: "tool", Tool: provider.Name(), DependsOn: []string{"first"}, Items: []any{"a", "b"}, Args: map[string]any{"text": "{{nodes.first.result}} {{item}}"}},
		{ID: "join", Type: "tool", Tool: provider.Name(), DependsOn: []string{"fanout"}, Args: map[string]any{"text": "{{nodes.fanout.result}}"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if view.Record.Status != "completed" {
		t.Fatalf("workflow failed: %#v", view.Nodes)
	}
	close(provider.received)
	got := map[string]bool{}
	for text := range provider.received {
		got[text] = true
	}
	for _, want := range []string{"hello", "hello a", "hello b", "hello a\nhello b"} {
		if !got[want] {
			t.Errorf("missing tool arguments %q: %v", want, got)
		}
	}
}

func TestDependenciesWaitForEveryFanoutItemAndPropagateSkips(t *testing.T) {
	if got := dependencyDecision(Node{DependsOn: []string{"fan"}}, map[string]string{"fan[0]": "completed", "fan[1]": ""}); got != "waiting" {
		t.Fatalf("fan-out join ran early: %s", got)
	}
	if got := dependencyDecision(Node{DependsOn: []string{"previous"}}, map[string]string{"previous": "skipped"}); got != "skipped" {
		t.Fatalf("skipped dependency ran: %s", got)
	}
}

func TestDefinitionBoundsExpandedTasksAndRejectsAliases(t *testing.T) {
	for _, nodes := range [][]Node{
		{{ID: "fan", Type: "agent", Prompt: "x", Items: []any{1, 2, 3}}},
		{{ID: "fan[0]", Type: "agent", Prompt: "x"}},
		{{ID: "invalid-timeout", Type: "agent", Prompt: "x", TimeoutSeconds: -1}},
	} {
		if err := (Definition{SchemaVersion: 1, Name: "invalid", Nodes: nodes}).Validate(2); err == nil {
			t.Fatalf("invalid nodes accepted: %#v", nodes)
		}
	}
}

func TestResumeReusesOnlyCompletedTasks(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry := tool.NewRegistry()
	provider := &retryTool{}
	if err := registry.Register(provider, tool.SourceBuiltin, "builtin"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(echoTool{}, tool.SourceBuiltin, "builtin"); err != nil {
		t.Fatal(err)
	}
	deny := true
	registry.SetPolicyCheck(func(_ context.Context, name string, _ map[string]any) error {
		if name == "echo" && deny {
			return fmt.Errorf("blocked")
		}
		return nil
	})
	starter := &fakeStarter{}
	cfg := config.DefaultConfig()
	cfg.Workflow.MaxAgentCalls = 1
	engine := NewEngine(db, starter, registry, cfg)
	view, err := engine.Start(context.Background(), Definition{SchemaVersion: 1, Name: "resume", Nodes: []Node{
		{ID: "completed", Type: "tool", Tool: provider.Name()},
		{ID: "gate", Type: "tool", Tool: "echo", DependsOn: []string{"completed"}},
		{ID: "agent", Type: "agent", Prompt: "hello", DependsOn: []string{"gate"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if view.Record.Status != "failed" {
		t.Fatalf("first run did not fail: %s", view.Record.Status)
	}
	deny = false
	view, err = engine.Resume(context.Background(), view.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Record.Status != "completed" || provider.calls.Load() != 1 || starter.calls.Load() != 1 {
		t.Fatalf("resume repeated a completed task: status=%s tool=%d agent=%d", view.Record.Status, provider.calls.Load(), starter.calls.Load())
	}
}

func TestResumeDoesNotResetAgentCallBudget(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	starter := &fakeStarter{fail: true}
	cfg := config.DefaultConfig()
	cfg.Workflow.MaxAgentCalls = 1
	engine := NewEngine(db, starter, tool.NewRegistry(), cfg)
	view, err := engine.Start(context.Background(), Definition{SchemaVersion: 1, Name: "budget", Nodes: []Node{{ID: "agent", Type: "agent", Prompt: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	starter.fail = false
	view, err = engine.Resume(context.Background(), view.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Record.Status != "failed" || starter.calls.Load() != 1 {
		t.Fatalf("resume reset budget: status=%s calls=%d", view.Record.Status, starter.calls.Load())
	}
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
