package tool

import (
	"context"
	"runtime"
	"testing"
)

func TestReadOnlyTerminalCommandIsAllowed(t *testing.T) {
	manager := NewPermissionManager("build")
	registered := &RegisteredTool{Provider: &testProvider{name: "run_terminal"}, Source: SourceBuiltin, Capabilities: Capabilities{
		Destructive: true, NeedsApproval: true, SideEffectScope: "system", RiskLevel: "high",
	}}
	if runtime.GOOS == "windows" {
		if err := manager.Authorize(context.Background(), registered, map[string]any{"command": "echo hello"}); err == nil {
			t.Fatal("POSIX classifier applied to cmd.exe")
		}
		return
	}
	if err := manager.Authorize(context.Background(), registered, map[string]any{"command": "ls -la"}); err != nil {
		t.Fatalf("read-only command was denied: %v", err)
	}
	for _, command := range []string{"rm -rf /tmp/x", `echo "$(touch marker)"`, "git status --short", "git branch injected"} {
		if err := manager.Authorize(context.Background(), registered, map[string]any{"command": command}); err == nil {
			t.Errorf("command was allowed without approval: %s", command)
		}
	}
}

func TestExternalToolCannotBorrowTerminalReadOnlyExemption(t *testing.T) {
	manager := NewPermissionManager("build")
	registered := &RegisteredTool{Provider: &testProvider{name: "run_terminal"}, Source: SourceMCP, Capabilities: Capabilities{NeedsApproval: true, RiskLevel: "high", SideEffectScope: "external"}}
	if err := manager.Authorize(context.Background(), registered, map[string]any{"command": "ls"}); err == nil {
		t.Fatal("external provider borrowed builtin terminal authorization")
	}
}

func TestReadOnlyRunConstraintCannotBeOverriddenByMode(t *testing.T) {
	manager := NewPermissionManager("yolo")
	for _, mode := range []string{"plan", "build", "edit", "guarded", "yolo"} {
		ctx := WithExecutionInfo(context.Background(), ExecutionInfo{Mode: mode, ReadOnly: true})
		write := &RegisteredTool{Provider: &testProvider{name: "write_file"}, Capabilities: Capabilities{SideEffectScope: "workspace"}}
		if err := manager.Authorize(ctx, write, nil); err == nil {
			t.Errorf("read-only run allowed a write in %s mode", mode)
		}
		read := &RegisteredTool{Provider: &testProvider{name: "read_file"}, Capabilities: Capabilities{ReadOnly: true, SideEffectScope: "workspace"}}
		if err := manager.Authorize(ctx, read, nil); err != nil {
			t.Errorf("read-only run denied a read in %s mode: %v", mode, err)
		}
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
