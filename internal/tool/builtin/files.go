package builtin

import (
	"agent-core/internal/sandbox"
	"agent-core/internal/tool"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileReadTool reads file contents.
type FileReadTool struct {
	workDir string
}

func NewFileReadTool(workDir string) *FileReadTool {
	return &FileReadTool{workDir: workDir}
}

func (t *FileReadTool) Name() string { return "read_file" }
func (t *FileReadTool) Description() string {
	return "Read the contents of a file at the given path. Returns the file content as text."
}
func (t *FileReadTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "The file path to read. Can be absolute or relative to work_dir.",
			},
		},
		Required: []string{"path"},
	}
}

func (t *FileReadTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return "", fmt.Errorf("path is required")
	}

	path, err := sandbox.ValidatePath(path, t.workDir, false)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	content := string(data)
	if len(content) > 50000 {
		content = content[:50000] + "\n... (truncated, total size: " + fmt.Sprintf("%d", len(data)) + " bytes)"
	}

	return content, nil
}

// FileWriteTool writes content to files.
type FileWriteTool struct {
	workDir string
}

func NewFileWriteTool(workDir string) *FileWriteTool {
	return &FileWriteTool{workDir: workDir}
}

func (t *FileWriteTool) Name() string { return "write_file" }
func (t *FileWriteTool) Description() string {
	return "Write content to a file. Creates parent directories if they don't exist."
}
func (t *FileWriteTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "The file path to write. Can be absolute or relative to work_dir.",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The content to write to the file.",
			},
			"append": map[string]any{
				"type":        "boolean",
				"description": "If true, append to existing file instead of overwriting. Default: false.",
			},
		},
		Required: []string{"path", "content"},
	}
}

func (t *FileWriteTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return "", fmt.Errorf("path is required")
	}
	content, ok := args["content"].(string)
	if !ok {
		return "", fmt.Errorf("content is required")
	}

	path, err := sandbox.ValidatePath(path, t.workDir, true)
	if err != nil {
		return "", err
	}

	// Create parent directories
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create directories: %w", err)
	}

	// Re-check for symlink immediately before open (narrows TOCTOU window)
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to write: %s is a symlink", path)
	}

	flag := os.O_WRONLY | os.O_CREATE | openFlagsNoFollow
	if appendMode, _ := args["append"].(bool); appendMode {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}

	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		// On Unix, O_NOFOLLOW returns ELOOP if the path is a symlink.
		if isSymlinkError(err) {
			return "", fmt.Errorf("refusing to write: %s is a symlink (O_NOFOLLOW)", path)
		}
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	// Post-open verification: confirm the opened file is a regular file, not a symlink.
	// f.Stat() follows symlinks, but os.Lstat on the path should agree.
	if linfo, err := os.Lstat(path); err == nil && linfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to write: %s became a symlink after open", path)
	}

	n, err := f.WriteString(content)
	if err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	return fmt.Sprintf("Written %d bytes to %s", n, path), nil
}

// FileListTool lists directory contents.
type FileListTool struct {
	workDir string
}

func NewFileListTool(workDir string) *FileListTool {
	return &FileListTool{workDir: workDir}
}

func (t *FileListTool) Name() string { return "list_files" }
func (t *FileListTool) Description() string {
	return "List files and directories at the given path."
}
func (t *FileListTool) Schema() tool.ToolSchema {
	return tool.ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "The directory path to list. Defaults to work_dir.",
			},
			"recursive": map[string]any{
				"type":        "boolean",
				"description": "If true, list recursively. Default: false.",
			},
		},
	}
}

func (t *FileListTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path := t.workDir
	if p, ok := args["path"].(string); ok && p != "" {
		var err error
		path, err = sandbox.ValidatePath(p, t.workDir, false)
		if err != nil {
			return "", err
		}
	}

	recursive, _ := args["recursive"].(bool)

	if recursive {
		return listRecursive(path, 3) // max depth 3
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("read directory: %w", err)
	}

	var sb strings.Builder
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		prefix := "  "
		if entry.IsDir() {
			prefix = "D "
		}
		fmt.Fprintf(&sb, "%s %8d  %s\n", prefix, info.Size(), entry.Name())
	}

	if sb.Len() == 0 {
		return "(empty directory)", nil
	}
	return sb.String(), nil
}

func listRecursive(root string, maxDepth int) (string, error) {
	var sb strings.Builder
	count := 0

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		depth := strings.Count(rel, string(filepath.Separator))
		if depth > maxDepth {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		prefix := "  "
		if info.IsDir() {
			prefix = "D "
		}
		fmt.Fprintf(&sb, "%s %8d  %s\n", prefix, info.Size(), rel)
		count++

		if count >= 500 {
			fmt.Fprintf(&sb, "... (limited to 500 entries)\n")
			return filepath.SkipAll
		}
		return nil
	})

	return sb.String(), err
}
