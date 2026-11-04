package agent

import (
	"agent-core/internal/model"
	"agent-core/internal/tool"
	"context"
	"fmt"
	"testing"
)

func TestZCodeMainRunExceedsFifteenToolCallsWithBoundedWarnings(t *testing.T) {
	provider := &fakeLLM{}
	for i := 0; i < 20; i++ {
		provider.completions = append(provider.completions, &model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: fmt.Sprintf("tool-%d", i), Type: "function", Function: model.ToolFunction{Name: "repeat", Arguments: "{}"}}}})
	}
	provider.completions = append(provider.completions, &model.Message{Role: model.RoleAssistant, Content: "finished twenty tools"})
	orchestrator, db, registry := newAgentHarness(t, provider)
	repeat := &agentTestTool{name: "repeat", readOnly: true}
	if err := registry.Register(repeat, tool.SourceBuiltin, "builtin"); err != nil {
		t.Fatal(err)
	}
	response, err := orchestrator.ChatSync(context.Background(), model.ChatRequest{Message: "continue until finished"})
	if err != nil || response.Reply != "finished twenty tools" || repeat.calls.Load() != 20 {
		t.Fatalf("main stopped early: %+v %v calls=%d", response, err, repeat.calls.Load())
	}
	run, _ := db.LatestRun(response.ConversationID)
	events, _ := db.RunEvents(run.ID, 0, 1000)
	warnings := 0
	for _, event := range events {
		if event.Type == "warning" {
			warnings++
		}
	}
	if warnings != 3 {
		t.Fatalf("want three advisory warnings, got %d", warnings)
	}
}
