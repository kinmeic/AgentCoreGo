package agent

import (
	"agent-core/internal/compressor"
	"agent-core/internal/config"
	"agent-core/internal/llm"
	"agent-core/internal/model"
	"agent-core/internal/prompt"
	"agent-core/internal/store"
	"agent-core/internal/tool"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLLM struct {
	mu           sync.Mutex
	completions  []*model.Message
	streams      [][]llm.StreamChunk
	completeFunc func(context.Context) (*model.Message, *model.UsageInfo, error)
}

func (f *fakeLLM) Complete(ctx context.Context, _ []model.Message, _ []model.ToolDefinition) (*model.Message, *model.UsageInfo, error) {
	if f.completeFunc != nil {
		return f.completeFunc(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var response *model.Message
	if len(f.completions) > 0 {
		response = f.completions[0]
		f.completions = f.completions[1:]
	}
	if response == nil {
		response = &model.Message{Role: model.RoleAssistant, Content: "done"}
	}
	return response, &model.UsageInfo{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}, nil
}
func (f *fakeLLM) CompleteStream(context.Context, []model.Message, []model.ToolDefinition) (<-chan llm.StreamChunk, error) {
	f.mu.Lock()
	var script []llm.StreamChunk
	if len(f.streams) > 0 {
		script = f.streams[0]
		f.streams = f.streams[1:]
	}
	f.mu.Unlock()
	ch := make(chan llm.StreamChunk, len(script))
	for _, chunk := range script {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}
func (f *fakeLLM) Name() string          { return "fake" }
func (f *fakeLLM) ModelName() string     { return "fake-model" }
func (f *fakeLLM) MaxContextWindow() int { return 128000 }

type agentTestTool struct {
	name     string
	readOnly bool
	calls    atomic.Int32
}

func (t *agentTestTool) Name() string        { return t.name }
func (t *agentTestTool) Description() string { return "test" }
func (t *agentTestTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Properties: map[string]any{}}
}
func (t *agentTestTool) Execute(context.Context, map[string]any) (string, error) {
	t.calls.Add(1)
	return "tool ok", nil
}
func (t *agentTestTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{ReadOnly: t.readOnly, NeedsApproval: !t.readOnly, SideEffectScope: "workspace"}
}

func newAgentHarness(t *testing.T, provider llm.Provider) (*Orchestrator, *store.SQLite, *tool.Registry) {
	t.Helper()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Workspace.Path = t.TempDir()
	cfg.Tools.WorkDir = cfg.Workspace.Path
	cfg.Tools.ApprovalMode = "yolo"
	registry := tool.NewRegistry()
	orchestrator := NewOrchestrator(provider, registry, tool.NewProcessRegistry(), db, nil,
		compressor.NewCompressor(cfg.Compression, nil), prompt.NewBuilder(), cfg)
	orchestrator.SetCheckpointStore(NewSQLiteCheckpointStore(db))
	t.Cleanup(func() { orchestrator.StopAll(); _ = db.Close() })
	return orchestrator, db, registry
}

func TestChatRejectsMissingConversation(t *testing.T) {
	orchestrator, _, _ := newAgentHarness(t, &fakeLLM{})
	_, err := orchestrator.ChatSync(context.Background(), model.ChatRequest{ConversationID: "missing", Message: "hello"})
	if !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("got %v, want ErrConversationNotFound", err)
	}
}

func TestChatStreamPersistsRunAndTerminatesWithDone(t *testing.T) {
	provider := &fakeLLM{streams: [][]llm.StreamChunk{{{Content: "hel"}, {Content: "lo"}}}}
	orchestrator, db, _ := newAgentHarness(t, provider)
	events := make(chan model.StreamEvent, 32)
	orchestrator.ChatStream(context.Background(), model.ChatRequest{Message: "hello"}, events)
	var runID string
	var done bool
	var lastSequence uint64
	for event := range events {
		if event.RunID == "" || event.Sequence <= lastSequence {
			t.Fatalf("invalid event envelope: %#v", event)
		}
		runID, lastSequence = event.RunID, event.Sequence
		if event.Type == "done" {
			done = true
		}
		if event.Type == "error" {
			t.Fatalf("unexpected error: %#v", event)
		}
	}
	if !done || runID == "" {
		t.Fatalf("missing terminal done, run=%q", runID)
	}
	run, err := db.GetRun(runID)
	if err != nil || run.Status != model.RunStatusCompleted || run.Reply != "hello" {
		t.Fatalf("unexpected run: %#v, %v", run, err)
	}
}

func TestStreamErrorNeverEmitsDone(t *testing.T) {
	provider := &fakeLLM{streams: [][]llm.StreamChunk{{{Content: "partial"}, {Err: errors.New("broken stream")}}}}
	orchestrator, db, _ := newAgentHarness(t, provider)
	events := make(chan model.StreamEvent, 32)
	orchestrator.ChatStream(context.Background(), model.ChatRequest{Message: "hello"}, events)
	var runID string
	var sawError bool
	for event := range events {
		runID = event.RunID
		if event.Type == "done" {
			t.Fatal("broken stream emitted done")
		}
		if event.Type == "error" {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("missing terminal error")
	}
	run, err := db.GetRun(runID)
	if err != nil || run.Status != model.RunStatusFailed {
		t.Fatalf("unexpected run status: %#v, %v", run, err)
	}
	for _, conversation := range []string{run.ConversationID} {
		logs, err := db.GetLogs(conversation, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range logs {
			if entry.Role == model.RoleAssistant {
				t.Fatal("partial assistant response was persisted as successful history")
			}
		}
	}
}

func TestStopRunCancelsModelCall(t *testing.T) {
	started := make(chan struct{})
	provider := &fakeLLM{completeFunc: func(ctx context.Context) (*model.Message, *model.UsageInfo, error) {
		close(started)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}}
	orchestrator, db, _ := newAgentHarness(t, provider)
	result := make(chan error, 1)
	go func() {
		_, err := orchestrator.ChatSync(context.Background(), model.ChatRequest{ConversationID: "", Message: "wait"})
		result <- err
	}()
	<-started
	var run *model.Run
	var err error
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		var id string
		if scanErr := db.DB().QueryRow(`SELECT id FROM runs ORDER BY created_at DESC LIMIT 1`).Scan(&id); scanErr == nil {
			run, err = db.GetRun(id)
		}
		if run != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if run == nil {
		t.Fatal("run was not persisted")
	}
	if !orchestrator.StopRun(run.ID) {
		t.Fatal("stop failed")
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context cancellation", err)
	}
	run, err = db.GetRun(run.ID)
	if err != nil || run.Status != model.RunStatusCancelled {
		t.Fatalf("unexpected stopped run: %#v, %v", run, err)
	}
}

func TestResumeRequiresStrategyForSideEffectingTool(t *testing.T) {
	provider := &fakeLLM{completions: []*model.Message{{Role: model.RoleAssistant, Content: "finished"}}}
	orchestrator, db, registry := newAgentHarness(t, provider)
	testTool := &agentTestTool{name: "write_test"}
	if err := registry.Register(testTool, tool.SourceBuiltin, "builtin"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.CreateConversation(&model.Conversation{ID: "conv", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run := &model.Run{ID: "run", ConversationID: "conv", Status: model.RunStatusFailed,
		Input: "test", CreatedAt: now, Usage: &model.UsageInfo{}}
	if err := db.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	call := model.ToolCall{ID: "call", Type: "function", Function: model.ToolFunction{Name: "write_test", Arguments: "{}"}}
	cp := &Checkpoint{ConversationID: "conv", RunID: "run", Messages: []model.Message{
		{Role: model.RoleUser, Content: "test"},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{call}},
	}, Pending: []model.ToolCall{call}, InFlight: &call}
	if err := orchestrator.checkpointStore.SaveCheckpoint(cp); err != nil {
		t.Fatal(err)
	}
	if _, err := orchestrator.ResumeRun(context.Background(), "run", "", false, nil); !errors.Is(err, ErrResumeToolRequired) {
		t.Fatalf("got %v, want explicit resume strategy error", err)
	}
	if testTool.calls.Load() != 0 {
		t.Fatal("side-effecting tool was replayed without consent")
	}
	response, err := orchestrator.ResumeRun(context.Background(), "run", "retry", false, nil)
	if err != nil {
		t.Fatalf("retry resume: %v", err)
	}
	if response.Status != model.RunStatusCompleted || testTool.calls.Load() != 1 {
		t.Fatalf("unexpected retry result: %#v, calls=%d", response, testTool.calls.Load())
	}
}
