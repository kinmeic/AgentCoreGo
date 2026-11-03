//go:build !windows

package builtin

import (
	"agent-core/internal/tool"
	"context"
	"strings"
	"testing"
)

func TestTerminalSessionEnvironment(t *testing.T) {
	workspace := testWorkspace(t)
	t.Setenv("LLM_API_KEY", "must-not-leak")
	toolContext := tool.WithExecutionInfo(context.Background(), tool.ExecutionInfo{ConversationID: "conv"})
	result, err := NewTerminalTool(workspace, nil).Execute(toolContext, map[string]any{
		"command": "printf '%s|%s|%s|%s|%s' \"$CLAUDE_CODE_SESSION_ID\" \"$ZCODE_SESSION_ID\" \"$CLAUDE_PROJECT_DIR\" \"$ZCODE_PROJECT_DIR\" \"$LLM_API_KEY\"",
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := "conv|conv|" + workspace + "|" + workspace + "|"
	if !strings.Contains(result, expected) {
		t.Fatalf("unexpected environment: %q, want %q", result, expected)
	}
	if strings.Contains(result, "must-not-leak") {
		t.Fatal("host credential leaked into terminal environment")
	}
}
