package builtin

import (
	"agent-core/internal/model"
	"agent-core/internal/sandbox"
	"agent-core/internal/tool"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxManagedFileBytes = 2 * 1024 * 1024

var fileLocks sync.Map

// FileReadTool reads file contents with a revision used for safe subsequent writes.
type FileReadTool struct{ workDir string }

func NewFileReadTool(workDir string) *FileReadTool { return &FileReadTool{workDir: workDir} }
func (t *FileReadTool) Name() string               { return "read_file" }
func (t *FileReadTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{ReadOnly: true, ConcurrentSafe: true, SideEffectScope: "workspace", RiskLevel: "low", Timeout: 30 * time.Second, MaxOutputBytes: 60_000}
}
func (t *FileReadTool) Description() string {
	return "Read a UTF-8 file. The revision header is metadata, not file content; pass it as expected_revision when overwriting. offset is 1-based."
}
func (t *FileReadTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"path"}, Properties: map[string]any{
		"path":   map[string]any{"type": "string"},
		"offset": map[string]any{"type": "integer", "minimum": 1},
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 2000},
	}}
}
func (t *FileReadTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path, err := sandbox.ValidatePath(args["path"].(string), t.workDir, false)
	if err != nil {
		return "", err
	}
	data, revision, err := readManagedFile(path)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("file is binary")
	}
	content := string(data)
	offset := intArg(args, "offset", 1)
	limit := intArg(args, "limit", 2000)
	if offset < 1 {
		offset = 1
	}
	lines := strings.Split(content, "\n")
	if offset > len(lines) {
		content = ""
	} else {
		end := offset - 1 + limit
		if limit <= 0 || end > len(lines) {
			end = len(lines)
		}
		content = strings.Join(lines[offset-1:end], "\n")
	}
	if len(content) > 50_000 {
		content = truncateUTF8(content, 50_000) + "\n... [truncated]"
	}
	return fmt.Sprintf("Revision: %s\n\n%s", revision, content), nil
}

// FileWriteTool atomically creates or overwrites files with optimistic concurrency checks.
type FileWriteTool struct{ workDir string }

func NewFileWriteTool(workDir string) *FileWriteTool { return &FileWriteTool{workDir: workDir} }
func (t *FileWriteTool) Name() string                { return "write_file" }
func (t *FileWriteTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{NeedsApproval: true, SideEffectScope: "workspace", RiskLevel: "medium", Timeout: 30 * time.Second, MaxOutputBytes: 100_000}
}
func (t *FileWriteTool) Description() string {
	return "Atomically create or overwrite a file. Overwriting requires expected_revision from read_file. Returns a JSON result with revision and diff."
}
func (t *FileWriteTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Required: []string{"path", "content"}, Properties: map[string]any{
		"path":              map[string]any{"type": "string"},
		"content":           map[string]any{"type": "string"},
		"append":            map[string]any{"type": "boolean"},
		"expected_revision": map[string]any{"type": "string"},
	}}
}
func (t *FileWriteTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, err := sandbox.ValidatePath(args["path"].(string), t.workDir, true)
	if err != nil {
		return "", err
	}
	content := args["content"].(string)
	appendMode, _ := args["append"].(bool)
	expected, _ := args["expected_revision"].(string)
	result, err := atomicWriteManagedFile(path, []byte(content), expected, appendMode)
	if err != nil {
		return "", err
	}
	if err := emitFileChange(ctx, path, "write"); err != nil {
		return result, err
	}
	return result, nil
}

func atomicWriteManagedFile(path string, content []byte, expectedRevision string, appendMode bool) (string, error) {
	if len(content) > maxManagedFileBytes {
		return "", fmt.Errorf("content exceeds %d bytes", maxManagedFileBytes)
	}
	lock := pathMutex(path)
	lock.Lock()
	defer lock.Unlock()
	var old []byte
	mode := os.FileMode(0o644)
	info, err := os.Lstat(path)
	exists := err == nil
	if exists {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("target is not a regular file")
		}
		mode = info.Mode().Perm()
		old, _, err = readManagedFile(path)
		if err != nil {
			return "", err
		}
		current := revisionFor(old)
		if expectedRevision == "" {
			return "", fmt.Errorf("existing file must be read first; expected_revision is required (current %s)", current)
		}
		if current != expectedRevision {
			return "", fmt.Errorf("file is stale: expected revision %s, current %s", expectedRevision, current)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else if expectedRevision != "" {
		return "", fmt.Errorf("file was created after the read; expected a new file")
	}
	if appendMode {
		if len(old)+len(content) > maxManagedFileBytes {
			return "", fmt.Errorf("appended file exceeds %d bytes", maxManagedFileBytes)
		}
		content = append(append([]byte(nil), old...), content...)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(dir, ".agent-write-*")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	closed := false
	defer func() {
		if !closed {
			_ = temp.Close()
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		return "", err
	}
	if _, err := temp.Write(content); err != nil {
		return "", err
	}
	if err := temp.Sync(); err != nil {
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	closed = true
	if exists {
		current, _, err := readManagedFile(path)
		if err != nil {
			return "", err
		}
		if revisionFor(current) != expectedRevision {
			return "", fmt.Errorf("file changed before atomic rename")
		}
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("target became a symlink")
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", fmt.Errorf("atomic rename: %w", err)
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	result := map[string]any{"path": path, "status": map[bool]string{true: "updated", false: "created"}[exists],
		"bytes": len(content), "revision": revisionFor(content), "diff": makeFileDiff(old, content)}
	encoded, _ := json.Marshal(result)
	return string(encoded), nil
}

func readManagedFile(path string) ([]byte, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", fmt.Errorf("read file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("not a regular file")
	}
	if info.Size() > maxManagedFileBytes {
		return nil, "", fmt.Errorf("file exceeds %d bytes", maxManagedFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read file: %w", err)
	}
	return data, revisionFor(data), nil
}

func revisionFor(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func pathMutex(path string) *sync.Mutex {
	value, _ := fileLocks.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func makeFileDiff(old, new []byte) map[string]any {
	oldLines, newLines := strings.Split(string(old), "\n"), strings.Split(string(new), "\n")
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix && oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	removed := oldLines[prefix : len(oldLines)-suffix]
	added := newLines[prefix : len(newLines)-suffix]
	var patch strings.Builder
	fmt.Fprintf(&patch, "@@ -%d,%d +%d,%d @@\n", prefix+1, len(removed), prefix+1, len(added))
	for _, line := range removed {
		patch.WriteString("-" + line + "\n")
	}
	for _, line := range added {
		patch.WriteString("+" + line + "\n")
	}
	return map[string]any{"before_lines": len(oldLines), "after_lines": len(newLines), "patch": patch.String()}
}

func emitFileChange(ctx context.Context, path, operation string) error {
	if info, ok := tool.ExecutionInfoFromContext(ctx); ok && info.Emit != nil {
		return info.Emit(model.StreamEvent{Type: "file.changed", Content: path, ToolName: operation})
	}
	return nil
}

func intArg(args map[string]any, name string, fallback int) int {
	if value, ok := args[name].(float64); ok {
		return int(value)
	}
	return fallback
}

func truncateUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	end := maximum
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

// FileListTool lists directory contents without following directory symlinks.
type FileListTool struct{ workDir string }

func NewFileListTool(workDir string) *FileListTool { return &FileListTool{workDir: workDir} }
func (t *FileListTool) Name() string               { return "list_files" }
func (t *FileListTool) Capabilities() tool.Capabilities {
	return tool.Capabilities{ReadOnly: true, ConcurrentSafe: true, SideEffectScope: "workspace", RiskLevel: "low", Timeout: 30 * time.Second, MaxOutputBytes: 100_000}
}
func (t *FileListTool) Description() string {
	return "List files and directories, optionally recursively."
}
func (t *FileListTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{Type: "object", Properties: map[string]any{
		"path": map[string]any{"type": "string"}, "recursive": map[string]any{"type": "boolean"},
		"max_entries": map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
	}}
}
func (t *FileListTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path := t.workDir
	if value, ok := args["path"].(string); ok && value != "" {
		var err error
		path, err = sandbox.ValidatePath(value, t.workDir, false)
		if err != nil {
			return "", err
		}
	}
	recursive, _ := args["recursive"].(bool)
	maximum := intArg(args, "max_entries", 500)
	if maximum <= 0 || maximum > 500 {
		maximum = 500
	}
	var entries []string
	if recursive {
		root := path
		err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if current == root {
				return nil
			}
			if len(entries) >= maximum {
				return fs.SkipAll
			}
			relative, _ := filepath.Rel(root, current)
			if entry.IsDir() {
				relative += string(filepath.Separator)
			}
			entries = append(entries, relative)
			return nil
		})
		if err != nil && err != fs.SkipAll {
			return "", err
		}
	} else {
		items, err := os.ReadDir(path)
		if err != nil {
			return "", err
		}
		for _, item := range items {
			if len(entries) >= maximum {
				break
			}
			name := item.Name()
			if item.IsDir() {
				name += string(filepath.Separator)
			}
			entries = append(entries, name)
		}
	}
	return strings.Join(entries, "\n"), nil
}
