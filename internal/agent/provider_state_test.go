package agent

import (
	"agent-core/internal/llm"
	"agent-core/internal/model"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCheckpointAndHistoryPreserveProviderState(t *testing.T) {
	orchestrator, db, _ := newAgentHarness(t, &fakeLLM{})
	now := time.Now()
	if err := db.CreateConversation(&model.Conversation{ID: "conv", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRun(&model.Run{ID: "run", ConversationID: "conv", Status: model.RunStatusRunning,
		Input: "test", CreatedAt: now, Usage: &model.UsageInfo{}}); err != nil {
		t.Fatal(err)
	}
	state := json.RawMessage(`{"content":[{"type":"thinking","thinking":"reasoning","signature":"signature"}]}`)
	checkpoint := &Checkpoint{ConversationID: "conv", RunID: "run", Messages: []model.Message{
		{Role: model.RoleAssistant, Content: "answer", ProviderState: state},
	}}
	if err := orchestrator.checkpointStore.SaveCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	loaded, err := orchestrator.checkpointStore.LoadCheckpoint("run", "conv")
	if err != nil || len(loaded.Messages) != 1 || string(loaded.Messages[0].ProviderState) != string(state) {
		t.Fatalf("checkpoint provider state mismatch: %#v, %v", loaded, err)
	}
	entry := &model.ConversationLog{Role: model.RoleAssistant, Content: "answer", ProviderState: state}
	if got := conversationLogMessage(entry); string(got.ProviderState) != string(state) {
		t.Fatalf("history provider state mismatch: %#v", got)
	}
}

func TestReasoningEventsArePublicButSeparateFromAnswer(t *testing.T) {
	state := json.RawMessage(`{"content":[{"type":"thinking","thinking":"embedded","signature":"signature"}]}`)
	provider := &fakeLLM{streams: [][]llm.StreamChunk{{
		{Reasoning: "native "},
		{Content: "before<th"},
		{Content: "ink>embedded"},
		{Content: "</think>after"},
		{ProviderState: state},
	}}}
	orchestrator, db, _ := newAgentHarness(t, provider)
	events := make(chan model.StreamEvent, 64)
	orchestrator.ChatStream(context.Background(), model.ChatRequest{Message: "hello"}, events)
	var runID, reasoning string
	var reasoningEvents int
	for event := range events {
		runID = event.RunID
		if event.Type == "reasoning" {
			reasoningEvents++
			reasoning += event.Content
		}
		if event.Type == "done" && event.Reply != "beforeafter" {
			t.Fatalf("reasoning leaked into final reply: %q", event.Reply)
		}
	}
	if reasoningEvents < 2 || reasoning != "native embedded" {
		t.Fatalf("unexpected reasoning events: count=%d content=%q", reasoningEvents, reasoning)
	}
	run, err := db.GetRun(runID)
	if err != nil || run == nil || run.Reply != "beforeafter" {
		t.Fatalf("unexpected run: %#v, %v", run, err)
	}
	logs, err := db.GetLogs(run.ConversationID, 20)
	if err != nil {
		t.Fatal(err)
	}
	var foundState bool
	for _, entry := range logs {
		if entry.Role == model.RoleAssistant {
			foundState = string(entry.ProviderState) == string(state)
		}
	}
	if !foundState {
		t.Fatal("assistant provider state was not persisted")
	}
	persisted, err := db.RunEvents(runID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var replayReasoning string
	for _, event := range persisted {
		if strings.Contains(string(event.Payload), "signature") {
			t.Fatalf("signature leaked into public event: %s", event.Payload)
		}
		var decoded model.StreamEvent
		if err := json.Unmarshal(event.Payload, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Type == "reasoning" {
			replayReasoning += decoded.Content
		}
	}
	if replayReasoning != "native embedded" {
		t.Fatalf("reasoning replay mismatch: %q", replayReasoning)
	}
}

func TestReasoningExposureCanBeDisabled(t *testing.T) {
	provider := &fakeLLM{streams: [][]llm.StreamChunk{{
		{Reasoning: "native"},
		{Content: "visible<think>hidden</think>answer"},
	}}}
	orchestrator, db, _ := newAgentHarness(t, provider)
	orchestrator.cfg.LLM.ExposeReasoning = false
	events := make(chan model.StreamEvent, 32)
	orchestrator.ChatStream(context.Background(), model.ChatRequest{Message: "hello"}, events)
	var runID string
	for event := range events {
		runID = event.RunID
		if event.Type == "reasoning" {
			t.Fatalf("reasoning emitted while disabled: %#v", event)
		}
	}
	run, err := db.GetRun(runID)
	if err != nil || run == nil || run.Reply != "visibleanswer" {
		t.Fatalf("unexpected filtered answer: %#v, %v", run, err)
	}
}
