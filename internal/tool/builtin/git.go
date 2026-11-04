package builtin

import (
	"agent-core/internal/sandbox"
	"agent-core/internal/tool"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// GitTool exposes a deliberately small, read-only Git surface.
type GitTool struct {
	workDir               string
	allowOutsideWorkspace bool
}

func NewGitTool(workDir string, allowOutsideWorkspace ...bool) *GitTool {
	return &GitTool{workDir: workDir, allowOutsideWorkspace: outsideWorkspaceEnabled(allowOutsideWorkspace)}
}
func (t *GitTool) Name() string { return "git_readonly" }
func (t *GitTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{ReadOnly: true, ConcurrentSafe: true, SideEffectScope: "workspace", RiskLevel: "low", Timeout: 35 * time.Second, MaxOutputBytes: 120_000}
}
func (t *GitTool) Description() string {
	return "Read Git status, diff, log, or branches. Never changes refs, index, working tree, hooks, or remotes."
}
func (t *GitTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"operation"}, Properties: map[string]any{
		"operation": map[string]any{"type": "string", "enum": []string{"status", "diff", "log", "branch"}},
		"path":      map[string]any{"type": "string"}, "ref": map[string]any{"type": "string"},
		"file": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
	}}
}
func (t *GitTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	workDir := t.workDir
	if path, ok := args["path"].(string); ok && path != "" {
		var err error
		workDir, err = sandbox.ValidateWorkingDirWithAccess(path, t.workDir, t.allowOutsideWorkspace)
		if err != nil {
			return "", err
		}
	}
	operation := args["operation"].(string)
	nullDevice := "/dev/null"
	if runtime.GOOS == "windows" {
		nullDevice = "NUL"
	}
	commandArgs := []string{"--no-pager", "-c", "safe.directory=" + workDir, "-c", "core.hooksPath=" + nullDevice,
		"-c", "core.fsmonitor=false", "-c", "diff.external=", "-c", "log.showSignature=false"}
	switch operation {
	case "status":
		commandArgs = append(commandArgs, "status", "--short", "--branch")
	case "diff":
		commandArgs = append(commandArgs, "diff", "--no-ext-diff", "--no-textconv")
	case "log":
		limit := intArg(args, "limit", 20)
		commandArgs = append(commandArgs, "log", "--oneline", "--decorate", "-n", strconv.Itoa(limit))
	case "branch":
		commandArgs = append(commandArgs, "branch", "--all", "--no-color")
	default:
		return "", fmt.Errorf("unsupported Git operation %q", operation)
	}
	if ref, ok := args["ref"].(string); ok && ref != "" && operation != "status" && operation != "branch" {
		if strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n") {
			return "", fmt.Errorf("invalid Git ref")
		}
		commandArgs = append(commandArgs, ref)
	}
	if file, ok := args["file"].(string); ok && file != "" && operation != "status" && operation != "branch" {
		if strings.HasPrefix(file, "-") || strings.ContainsAny(file, "\x00\r\n") {
			return "", fmt.Errorf("invalid Git file")
		}
		commandArgs = append(commandArgs, "--", file)
	}
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "git", commandArgs...)
	cmd.Dir = workDir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + nullDevice, "GIT_CONFIG_SYSTEM=" + nullDevice,
		"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "LC_ALL=C"}
	stdout, stderr := &boundedOutput{maximum: 100_000}, &boundedOutput{maximum: 20_000}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	result := stdout.String()
	if stderr.Len() > 0 {
		result += "\nSTDERR:\n" + stderr.String()
	}
	if commandCtx.Err() != nil {
		return result, commandCtx.Err()
	}
	if err != nil {
		return result, fmt.Errorf("git %s failed: %w", operation, err)
	}
	return result, nil
}

type boundedOutput struct {
	value     strings.Builder
	maximum   int
	truncated bool
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	remaining := b.maximum - b.value.Len()
	if remaining > 0 {
		if len(data) <= remaining {
			b.value.Write(data)
		} else {
			b.value.Write(data[:remaining])
			b.truncated = true
		}
	} else if len(data) > 0 {
		b.truncated = true
	}
	return len(data), nil
}
func (b *boundedOutput) String() string {
	if b.truncated {
		return b.value.String() + "\n... [truncated]"
	}
	return b.value.String()
}
func (b *boundedOutput) Len() int { return b.value.Len() }
