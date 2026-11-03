package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testWorkspace(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAtomicWriteRequiresCurrentRevision(t *testing.T) {
	workspace := testWorkspace(t)
	writer := NewFileWriteTool(workspace)
	result, err := writer.Execute(context.Background(), map[string]any{"path": "note.txt", "content": "one\n"})
	if err != nil {
		t.Fatal(err)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(result), &created); err != nil {
		t.Fatalf("write result is not JSON: %v", err)
	}
	if _, err := writer.Execute(context.Background(), map[string]any{"path": "note.txt", "content": "two"}); err == nil {
		t.Fatal("overwrite without a read revision succeeded")
	}
	if _, err := writer.Execute(context.Background(), map[string]any{"path": "note.txt", "content": "two", "expected_revision": "stale"}); err == nil {
		t.Fatal("stale overwrite succeeded")
	}
	data, _ := os.ReadFile(filepath.Join(workspace, "note.txt"))
	if string(data) != "one\n" {
		t.Fatalf("failed writes changed content: %q", data)
	}
	read, err := NewFileReadTool(workspace).Execute(context.Background(), map[string]any{"path": "note.txt"})
	if err != nil {
		t.Fatal(err)
	}
	revision := strings.TrimPrefix(strings.SplitN(read, "\n", 2)[0], "Revision: ")
	if _, err := writer.Execute(context.Background(), map[string]any{"path": "note.txt", "content": "two\n", "expected_revision": revision}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(workspace, "note.txt"))
	if string(data) != "two\n" {
		t.Fatalf("atomic write content = %q", data)
	}
}

func TestEditAndMoveValidateRevisions(t *testing.T) {
	workspace := testWorkspace(t)
	path := filepath.Join(workspace, "a.txt")
	if err := os.WriteFile(path, []byte("alpha beta"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, revision, err := readManagedFile(path)
	if err != nil {
		t.Fatal(err)
	}
	editor := NewFileEditTool(workspace)
	if _, err := editor.Execute(context.Background(), map[string]any{"path": "a.txt", "old_string": "beta", "new_string": "gamma", "expected_revision": revision}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileMoveTool(workspace).Execute(context.Background(), map[string]any{"source": "a.txt", "destination": "a.txt", "expected_revision": revision}); err == nil {
		t.Fatal("identical move should fail")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "alpha gamma" {
		t.Fatalf("unexpected edit: %q", data)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("file permissions were not preserved: %v, %v", info, err)
	}
}

func TestGlobAndGrep(t *testing.T) {
	workspace := testWorkspace(t)
	if err := os.MkdirAll(filepath.Join(workspace, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"root.go": "package root", "nested/main.go": "needle here", "nested/readme.txt": "needle text"} {
		if err := os.WriteFile(filepath.Join(workspace, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	matches, err := NewGlobTool(workspace).Execute(context.Background(), map[string]any{"pattern": "**/*.go"})
	if err != nil || !strings.Contains(matches, "root.go") || !strings.Contains(matches, "nested/main.go") {
		t.Fatalf("unexpected glob: %q, %v", matches, err)
	}
	matches, err = NewGrepTool(workspace).Execute(context.Background(), map[string]any{"query": "needle", "glob": "**/*.go"})
	if err != nil || !strings.Contains(matches, "nested/main.go:1:needle here") || strings.Contains(matches, "readme") {
		t.Fatalf("unexpected grep: %q, %v", matches, err)
	}
}
