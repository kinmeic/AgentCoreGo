package tool

import (
	"context"
	"testing"
	"time"

	"agent-core/internal/model"
)

func TestPlanModeDeniesWrites(t *testing.T) {
	manager := NewPermissionManager("plan")
	registered := &RegisteredTool{Provider: &testProvider{name: "write"}, Capabilities: Capabilities{}}
	if err := manager.Authorize(context.Background(), registered, nil); err == nil {
		t.Fatal("expected plan mode to deny a write")
	}
}

func TestApprovalBrokerLifecycle(t *testing.T) {
	manager := NewPermissionManager("build")
	registered := &RegisteredTool{Provider: &testProvider{name: "write"}, Capabilities: Capabilities{
		NeedsApproval: true, SideEffectScope: "workspace", RiskLevel: "medium",
	}}
	events := make(chan model.StreamEvent, 1)
	ctx := WithExecutionInfo(context.Background(), ExecutionInfo{
		ConversationID: "conv", RunID: "run", ToolCallID: "call", Interactive: true,
		Emit: func(event model.StreamEvent) error { events <- event; return nil },
	})
	result := make(chan error, 1)
	go func() { result <- manager.Authorize(ctx, registered, map[string]any{"path": "a"}) }()
	var event model.StreamEvent
	select {
	case event = <-events:
	case <-time.After(time.Second):
		t.Fatal("approval event was not emitted")
	}
	if event.Type != "approval.required" || event.RequestID == "" {
		t.Fatalf("unexpected event: %#v", event)
	}
	if !manager.Respond(event.RequestID, ApprovalResponse{Decision: "allow", Scope: "conversation"}) {
		t.Fatal("failed to resolve approval")
	}
	if manager.Respond(event.RequestID, ApprovalResponse{Decision: "deny"}) {
		t.Fatal("duplicate response unexpectedly succeeded")
	}
	if err := <-result; err != nil {
		t.Fatalf("approved call failed: %v", err)
	}
	if err := manager.Authorize(ctx, registered, nil); err != nil {
		t.Fatalf("conversation grant was not reused: %v", err)
	}
}

func TestApprovalRequiresInteractiveClient(t *testing.T) {
	manager := NewPermissionManager("build")
	registered := &RegisteredTool{Provider: &testProvider{name: "write"}, Capabilities: Capabilities{NeedsApproval: true}}
	if err := manager.Authorize(context.Background(), registered, nil); err == nil {
		t.Fatal("expected non-interactive execution to fail closed")
	}
}
