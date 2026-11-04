package builtin

import (
	"agent-core/internal/sandbox"
	"agent-core/internal/tool"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileEditTool performs exact, revision-checked string replacement.
type FileEditTool struct {
	workDir               string
	allowOutsideWorkspace bool
}

func NewFileEditTool(workDir string, allowOutsideWorkspace ...bool) *FileEditTool {
	return &FileEditTool{workDir: workDir, allowOutsideWorkspace: outsideWorkspaceEnabled(allowOutsideWorkspace)}
}
func (t *FileEditTool) Name() string { return "edit_file" }
func (t *FileEditTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{NeedsApproval: true, SideEffectScope: "workspace", RiskLevel: "medium", Timeout: 30 * time.Second}
}
func (t *FileEditTool) Description() string {
	return "Replace exact text in a UTF-8 file using expected_revision from read_file. A single replacement must match exactly once unless replace_all is true."
}
func (t *FileEditTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"path", "old_string", "new_string", "expected_revision"}, Properties: map[string]any{
		"path": map[string]any{"type": "string"}, "old_string": map[string]any{"type": "string", "minLength": 1},
		"new_string": map[string]any{"type": "string"}, "expected_revision": map[string]any{"type": "string"},
		"replace_all": map[string]any{"type": "boolean"},
	}}
}
func (t *FileEditTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, err := sandbox.ValidatePathWithAccess(args["path"].(string), t.workDir, false, t.allowOutsideWorkspace)
	if err != nil {
		return "", err
	}
	data, _, err := readManagedFile(path)
	if err != nil {
		return "", err
	}
	oldText := args["old_string"].(string)
	newText := args["new_string"].(string)
	content := string(data)
	count := strings.Count(content, oldText)
	replaceAll, _ := args["replace_all"].(bool)
	if count == 0 || (count != 1 && !replaceAll) {
		return "", fmt.Errorf("old_string matched %d times; want exactly one or replace_all=true", count)
	}
	if replaceAll {
		content = strings.ReplaceAll(content, oldText, newText)
	} else {
		content = strings.Replace(content, oldText, newText, 1)
	}
	result, err := atomicWriteManagedFile(path, []byte(content), args["expected_revision"].(string), false)
	if err != nil {
		return "", err
	}
	if err := emitFileChange(ctx, path, "edit", result); err != nil {
		return result, err
	}
	return result, nil
}

// FileDeleteTool deletes one regular file after verifying its revision.
type FileDeleteTool struct {
	workDir               string
	allowOutsideWorkspace bool
}

func NewFileDeleteTool(workDir string, allowOutsideWorkspace ...bool) *FileDeleteTool {
	return &FileDeleteTool{workDir: workDir, allowOutsideWorkspace: outsideWorkspaceEnabled(allowOutsideWorkspace)}
}
func (t *FileDeleteTool) Name() string { return "delete_file" }
func (t *FileDeleteTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{Destructive: true, NeedsApproval: true, SideEffectScope: "workspace", RiskLevel: "high", Timeout: 30 * time.Second}
}
func (t *FileDeleteTool) Description() string {
	return "Delete one regular file after expected_revision validation."
}
func (t *FileDeleteTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"path", "expected_revision"}, Properties: map[string]any{
		"path": map[string]any{"type": "string"}, "expected_revision": map[string]any{"type": "string"},
	}}
}
func (t *FileDeleteTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, err := sandbox.ValidatePathWithAccess(args["path"].(string), t.workDir, false, t.allowOutsideWorkspace)
	if err != nil {
		return "", err
	}
	lock := pathMutex(path)
	lock.Lock()
	defer lock.Unlock()
	data, revision, err := readManagedFile(path)
	_ = data
	if err != nil {
		return "", err
	}
	if revision != args["expected_revision"].(string) {
		return "", fmt.Errorf("file is stale: current revision %s", revision)
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	if err := emitFileChange(ctx, path, "delete", fmt.Sprintf(`{"before_revision":%q}`, revision)); err != nil {
		return "", err
	}
	return fmt.Sprintf("Deleted %s", path), nil
}

// FileMoveTool renames a file without overwriting an existing destination.
type FileMoveTool struct {
	workDir               string
	allowOutsideWorkspace bool
}

func NewFileMoveTool(workDir string, allowOutsideWorkspace ...bool) *FileMoveTool {
	return &FileMoveTool{workDir: workDir, allowOutsideWorkspace: outsideWorkspaceEnabled(allowOutsideWorkspace)}
}
func (t *FileMoveTool) Name() string { return "move_file" }
func (t *FileMoveTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{Destructive: true, NeedsApproval: true, SideEffectScope: "workspace", RiskLevel: "high", Timeout: 30 * time.Second}
}
func (t *FileMoveTool) Description() string {
	return "Move a regular file; relative paths start in the workspace and destination must not exist."
}
func (t *FileMoveTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"source", "destination", "expected_revision"}, Properties: map[string]any{
		"source": map[string]any{"type": "string"}, "destination": map[string]any{"type": "string"},
		"expected_revision": map[string]any{"type": "string"},
	}}
}
func (t *FileMoveTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	source, err := sandbox.ValidatePathWithAccess(args["source"].(string), t.workDir, false, t.allowOutsideWorkspace)
	if err != nil {
		return "", err
	}
	destination, err := sandbox.ValidatePathWithAccess(args["destination"].(string), t.workDir, true, t.allowOutsideWorkspace)
	if err != nil {
		return "", err
	}
	if source == destination {
		return "", fmt.Errorf("source and destination are identical")
	}
	first, second := source, destination
	if second < first {
		first, second = second, first
	}
	firstLock, secondLock := pathMutex(first), pathMutex(second)
	firstLock.Lock()
	defer firstLock.Unlock()
	secondLock.Lock()
	defer secondLock.Unlock()
	_, revision, err := readManagedFile(source)
	if err != nil {
		return "", err
	}
	if revision != args["expected_revision"].(string) {
		return "", fmt.Errorf("source is stale: current revision %s", revision)
	}
	if _, err := os.Lstat(destination); err == nil {
		return "", fmt.Errorf("destination already exists")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(source, destination); err != nil {
		return "", err
	}
	if err := emitFileChange(ctx, source+" -> "+destination, "move", fmt.Sprintf(`{"before_revision":%q}`, revision)); err != nil {
		return "", err
	}
	return fmt.Sprintf("Moved %s to %s", source, destination), nil
}
