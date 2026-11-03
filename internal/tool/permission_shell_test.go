package tool

import (
	"context"
	"testing"
)

func TestReadOnlyTerminalCommandIsAllowed(t *testing.T) {
	manager := NewPermissionManager("build")
	registered := &RegisteredTool{Provider: &testProvider{name: "run_terminal"}, Capabilities: Capabilities{
		Destructive: true, NeedsApproval: true, SideEffectScope: "system", RiskLevel: "high",
	}}
	if err := manager.Authorize(context.Background(), registered, map[string]any{"command": "git status --short"}); err != nil {
		t.Fatalf("read-only Git command was denied: %v", err)
	}
	if err := manager.Authorize(context.Background(), registered, map[string]any{"command": "rm -rf /tmp/x"}); err == nil {
		t.Fatal("mutating command was allowed without approval")
	}
}

func TestPlanDoesNotWidenReadOnlyApprovalBoundary(t *testing.T) {
	manager := NewPermissionManager("plan")
	readOnlyNeedsApproval := Capabilities{ReadOnly: true, NeedsApproval: true, SideEffectScope: "external", RiskLevel: "high"}
	if decision := manager.decisionForMode("plan", readOnlyNeedsApproval); decision != "ask" {
		t.Fatalf("read-only external tool should ask under plan, got %s", decision)
	}
	readOnlyQuery := Capabilities{ReadOnly: true, NeedsApproval: false, SideEffectScope: "workspace", RiskLevel: "low"}
	if decision := manager.decisionForMode("plan", readOnlyQuery); decision != "allow" {
		t.Fatalf("read-only query should be allowed, got %s", decision)
	}
	mutating := Capabilities{NeedsApproval: true, SideEffectScope: "workspace"}
	if decision := manager.decisionForMode("plan", mutating); decision != "deny" {
		t.Fatalf("mutating builtin should be denied, got %s", decision)
	}
}
