package tool

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-core/internal/model"
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

func terminalPermissionTool() *RegisteredTool {
	return &RegisteredTool{Provider: &testProvider{name: "run_terminal"}, Source: SourceBuiltin, Capabilities: Capabilities{
		Destructive: true, NeedsApproval: true, SideEffectScope: "system", RiskLevel: "high",
	}}
}

func authorizeTerminalWithApproval(t *testing.T, manager *PermissionManager, registered *RegisteredTool, scope string) {
	t.Helper()
	events := make(chan model.StreamEvent, 1)
	ctx := WithExecutionInfo(context.Background(), ExecutionInfo{
		ConversationID: "conv", RunID: "run", ToolCallID: "call", Interactive: true,
		Emit: func(event model.StreamEvent) error { events <- event; return nil },
	})
	result := make(chan error, 1)
	go func() {
		result <- manager.Authorize(ctx, registered, map[string]any{"command": "touch marker"})
	}()
	select {
	case event := <-events:
		if event.Type != "approval.required" || event.RequestID == "" {
			t.Fatalf("unexpected event: %#v", event)
		}
		if !manager.Respond(event.RequestID, ApprovalResponse{Decision: "allow", Scope: scope}) {
			t.Fatal("failed to resolve terminal approval")
		}
	case <-time.After(time.Second):
		t.Fatal("terminal approval event was not emitted")
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("approved terminal command failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal authorization did not finish")
	}
}

func TestAlwaysAskTerminalOverridesAutomaticAllow(t *testing.T) {
	for _, mode := range []string{"build", "edit", "guarded", "yolo"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewPermissionManager(mode)
			manager.ConfigureRules([]string{"run_terminal"}, nil)
			manager.SetAutoApproveHighRisk(true)
			manager.SetAlwaysAskTerminal(true)
			authorizeTerminalWithApproval(t, manager, terminalPermissionTool(), "once")
		})
	}
}

func TestAlwaysAskTerminalDoesNotReuseConversationGrant(t *testing.T) {
	manager := NewPermissionManager("yolo")
	manager.SetAlwaysAskTerminal(true)
	registered := terminalPermissionTool()
	authorizeTerminalWithApproval(t, manager, registered, "conversation")
	authorizeTerminalWithApproval(t, manager, registered, "conversation")
}

func TestAlwaysAskTerminalCanBeDisabled(t *testing.T) {
	manager := NewPermissionManager("yolo")
	manager.SetAlwaysAskTerminal(true)
	registered := terminalPermissionTool()
	authorizeTerminalWithApproval(t, manager, registered, "once")
	manager.SetAlwaysAskTerminal(false)
	if err := manager.Authorize(context.Background(), registered, map[string]any{"command": "touch marker"}); err != nil {
		t.Fatalf("disabled alwaysAskTerminal still blocked yolo: %v", err)
	}
}

func TestAlwaysAskTerminalDoesNotUpgradeDeny(t *testing.T) {
	for _, mode := range []string{"plan", "auto", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewPermissionManager(mode)
			manager.ConfigureRules([]string{"run_terminal"}, nil)
			manager.SetAutoApproveHighRisk(true)
			manager.SetAlwaysAskTerminal(true)
			events := make(chan model.StreamEvent, 1)
			ctx := WithExecutionInfo(context.Background(), ExecutionInfo{
				Interactive: true, Emit: func(event model.StreamEvent) error { events <- event; return nil },
			})
			err := manager.Authorize(ctx, terminalPermissionTool(), map[string]any{"command": "touch marker"})
			if err == nil || !strings.Contains(err.Error(), "permission denied in "+mode+" mode") {
				t.Fatalf("expected %s deny, got %v", mode, err)
			}
			select {
			case event := <-events:
				t.Fatalf("deny was upgraded to an event: %#v", event)
			default:
			}
		})
	}
}

func TestAlwaysAskTerminalKeepsReadOnlyRunDeny(t *testing.T) {
	manager := NewPermissionManager("yolo")
	manager.SetAlwaysAskTerminal(true)
	events := make(chan model.StreamEvent, 1)
	ctx := WithExecutionInfo(context.Background(), ExecutionInfo{
		ReadOnly: true, Interactive: true,
		Emit: func(event model.StreamEvent) error { events <- event; return nil },
	})
	err := manager.Authorize(ctx, terminalPermissionTool(), map[string]any{"command": "touch marker"})
	if err == nil || !strings.Contains(err.Error(), "run is read-only") {
		t.Fatalf("expected read-only run deny, got %v", err)
	}
	select {
	case event := <-events:
		t.Fatalf("read-only run deny was upgraded to an event: %#v", event)
	default:
	}
}

func TestAlwaysAskTerminalKeepsExplicitDeny(t *testing.T) {
	manager := NewPermissionManager("yolo")
	manager.ConfigureRules([]string{"run_terminal"}, []string{"run_terminal"})
	manager.SetAlwaysAskTerminal(true)
	err := manager.Authorize(context.Background(), terminalPermissionTool(), map[string]any{"command": "touch marker"})
	if err == nil || !strings.Contains(err.Error(), "permission.disallowedTools") {
		t.Fatalf("expected explicit deny, got %v", err)
	}
}

func TestAlwaysAskTerminalReadOnlyExemption(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX read-only shell classifier is not used on Windows")
	}
	for _, mode := range []string{"plan", "build", "edit", "guarded", "yolo"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewPermissionManager(mode)
			manager.SetAlwaysAskTerminal(true)
			if err := manager.Authorize(context.Background(), terminalPermissionTool(), map[string]any{"command": "ls -la"}); err != nil {
				t.Fatalf("read-only terminal command was denied in %s mode: %v", mode, err)
			}
		})
	}
}
