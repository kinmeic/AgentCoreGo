package builtin

import (
	"agent-core/internal/sandbox"
	"agent-core/internal/tool"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// TerminalTool executes shell commands.
type TerminalTool struct {
	workDir         string
	workspaceDir    string
	timeout         time.Duration
	processRegistry *tool.ProcessRegistry
}

func NewTerminalTool(workDir string, processRegistry *tool.ProcessRegistry) *TerminalTool {
	return &TerminalTool{
		workDir:         workDir,
		workspaceDir:    workDir,
		timeout:         120 * time.Second,
		processRegistry: processRegistry,
	}
}

func (t *TerminalTool) Name() string { return "run_terminal" }
func (t *TerminalTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{Destructive: true, NeedsApproval: true, SideEffectScope: "system", RiskLevel: "high", Timeout: 610 * time.Second, MaxOutputBytes: 100_000}
}
func (t *TerminalTool) Description() string {
	return "Execute a shell command in the terminal. Returns stdout and stderr output."
}
func (t *TerminalTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute.",
			},
			"working_dir": map[string]any{
				"type":        "string",
				"description": "Working directory for the command. Defaults to configured work_dir.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Timeout in seconds. Default 120, max 600.",
			},
		},
		Required: []string{"command"},
	}
}

func (t *TerminalTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return "", fmt.Errorf("command is required")
	}

	workDir := t.workDir
	if wd, ok := args["working_dir"].(string); ok && wd != "" {
		validated, err := sandbox.ValidateWorkingDir(wd, t.workspaceDir)
		if err != nil {
			return "", fmt.Errorf("invalid working_dir: %w", err)
		}
		workDir = validated
	}

	timeout := t.timeout
	if ts, ok := args["timeout_seconds"].(float64); ok && ts > 0 {
		if ts > 600 {
			ts = 600
		}
		timeout = time.Duration(ts) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell := "/bin/sh"
	shellFlag := "-c"
	if runtime.GOOS == "windows" {
		shell = "cmd"
		shellFlag = "/C"
	}

	cmd := exec.CommandContext(ctx, shell, shellFlag, command)
	tool.PrepareProcess(cmd)
	cmd.WaitDelay = 5 * time.Second
	if workDir != "" {
		cmd.Dir = workDir
	}

	info, _ := tool.ExecutionInfoFromContext(ctx)
	cmd.Env = terminalEnvironment(info, t.workspaceDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Register only after Start publishes cmd.Process, then wait concurrently with cancellation.
	convID := info.ConversationID
	var procID string
	startErr := cmd.Start()
	if startErr == nil && t.processRegistry != nil {
		procID = t.processRegistry.Register(cmd, "run_terminal", convID, timeout)
		defer t.processRegistry.Unregister(procID)
	}

	var result strings.Builder
	var err error
	if startErr != nil {
		err = startErr
	} else {
		err = cmd.Wait()
	}

	if stdout.Len() > 0 {
		result.WriteString("STDOUT:\n")
		output := stdout.String()
		if len(output) > 10000 {
			runes := []rune(output)
			if len(runes) > 10000 {
				output = string(runes[:10000]) + "\n... (truncated)"
			}
		}
		result.WriteString(output)
	}
	if stderr.Len() > 0 {
		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString("STDERR:\n")
		output := stderr.String()
		if len(output) > 5000 {
			runes := []rune(output)
			if len(runes) > 5000 {
				output = string(runes[:5000]) + "\n... (truncated)"
			}
		}
		result.WriteString(output)
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return result.String(), fmt.Errorf("command timed out after %v", timeout)
		}
		if result.Len() == 0 {
			return "", fmt.Errorf("command failed: %w", err)
		}
		// Return output with exit status AND error so callers know command failed
		result.WriteString(fmt.Sprintf("\n[exit status: %v]", err))
		return result.String(), fmt.Errorf("command failed (exit %v)", err)
	}

	if result.Len() == 0 {
		return "(command completed with no output)", nil
	}

	return result.String(), nil
}

func terminalEnvironment(info tool.ExecutionInfo, workspace string) []string {
	values := make(map[string]string)
	for _, key := range []string{"PATH", "HOME", "USERPROFILE", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP", "TMPDIR"} {
		if value := os.Getenv(key); value != "" {
			values[key] = value
		}
	}
	for _, key := range []string{"CLAUDE_CODE_SESSION_ID", "CLAUDE_SESSION_ID", "ZCODE_SESSION_ID"} {
		values[key] = info.ConversationID
	}
	workspace = filepath.ToSlash(workspace)
	values["CLAUDE_PROJECT_DIR"] = workspace
	values["ZCODE_PROJECT_DIR"] = workspace
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}
