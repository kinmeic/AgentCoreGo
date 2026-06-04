package builtin

import (
	"agent-core/internal/sandbox"
	"agent-core/internal/tool"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// TerminalTool executes shell commands.
type TerminalTool struct {
	workDir      string
	workspaceDir string
	timeout      time.Duration
}

func NewTerminalTool(workDir string) *TerminalTool {
	return &TerminalTool{
		workDir:      workDir,
		workspaceDir: workDir,
		timeout:      120 * time.Second,
	}
}

func (t *TerminalTool) Name() string { return "run_terminal" }
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
	if workDir != "" {
		cmd.Dir = workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	var result strings.Builder
	if stdout.Len() > 0 {
		result.WriteString("STDOUT:\n")
		output := stdout.String()
		if len(output) > 10000 {
			output = output[:10000] + "\n... (truncated)"
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
			output = output[:5000] + "\n... (truncated)"
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
		// Return output even on error (e.g., non-zero exit code with useful output)
		return result.String(), nil
	}

	if result.Len() == 0 {
		return "(command completed with no output)", nil
	}

	return result.String(), nil
}
