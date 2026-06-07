package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// setupTestWorkspace creates a temporary workspace directory and returns
// its path and a cleanup function.
func setupTestWorkspace(t *testing.T) (string, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sandbox-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	// Resolve symlinks (macOS /var -> /private/var)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("eval symlinks: %v", err)
	}
	return resolved, func() { os.RemoveAll(resolved) }
}

func TestValidatePath_NormalRelative(t *testing.T) {
	ws, cleanup := setupTestWorkspace(t)
	defer cleanup()

	// Create a file in the workspace
	os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hi"), 0o644)

	resolved, err := ValidatePath("hello.txt", ws, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(ws, "hello.txt")
	if resolved != expected {
		t.Errorf("got %q, want %q", resolved, expected)
	}
}

func TestValidatePath_NormalAbsolute(t *testing.T) {
	ws, cleanup := setupTestWorkspace(t)
	defer cleanup()

	os.WriteFile(filepath.Join(ws, "data.txt"), []byte("x"), 0o644)

	target := filepath.Join(ws, "data.txt")
	resolved, err := ValidatePath(target, ws, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != target {
		t.Errorf("got %q, want %q", resolved, target)
	}
}

func TestValidatePath_PathTraversal(t *testing.T) {
	ws, cleanup := setupTestWorkspace(t)
	defer cleanup()

	_, err := ValidatePath("../../../etc/passwd", ws, false)
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestValidatePath_AbsoluteOutside(t *testing.T) {
	ws, cleanup := setupTestWorkspace(t)
	defer cleanup()

	_, err := ValidatePath("/etc/passwd", ws, false)
	if err == nil {
		t.Fatal("expected error for absolute path outside workspace, got nil")
	}
}

func TestValidatePath_SymlinkEscapeRead(t *testing.T) {
	ws, cleanup := setupTestWorkspace(t)
	defer cleanup()

	// Create a file outside the workspace
	outsideDir, err := os.MkdirTemp("", "sandbox-outside-*")
	if err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	defer os.RemoveAll(outsideDir)

	outsideFile := filepath.Join(outsideDir, "secret.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0o644)

	// Create a symlink inside workspace pointing outside
	linkPath := filepath.Join(ws, "link-to-secret")
	os.Symlink(outsideFile, linkPath)

	_, err = ValidatePath("link-to-secret", ws, false)
	if err == nil {
		t.Fatal("expected error for symlink pointing outside workspace (read), got nil")
	}
}

func TestValidatePath_SymlinkEscapeWrite(t *testing.T) {
	ws, cleanup := setupTestWorkspace(t)
	defer cleanup()

	// Create a file outside the workspace
	outsideDir, err := os.MkdirTemp("", "sandbox-outside-*")
	if err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	defer os.RemoveAll(outsideDir)

	outsideFile := filepath.Join(outsideDir, "target.txt")
	os.WriteFile(outsideFile, []byte("original"), 0o644)

	// Create a symlink inside workspace pointing outside
	linkPath := filepath.Join(ws, "evil-link")
	os.Symlink(outsideFile, linkPath)

	_, err = ValidatePath("evil-link", ws, true)
	if err == nil {
		t.Fatal("expected error for symlink pointing outside workspace (write), got nil")
	}
}

func TestValidatePath_SymlinkInsideWorkspace(t *testing.T) {
	ws, cleanup := setupTestWorkspace(t)
	defer cleanup()

	// Create a real file inside workspace
	realFile := filepath.Join(ws, "real.txt")
	os.WriteFile(realFile, []byte("real"), 0o644)

	// Create a symlink inside workspace pointing to another file inside workspace
	linkPath := filepath.Join(ws, "good-link")
	os.Symlink(realFile, linkPath)

	// Read: symlink within workspace should be allowed
	resolved, err := ValidatePath("good-link", ws, false)
	if err != nil {
		t.Fatalf("unexpected error for internal symlink (read): %v", err)
	}
	if resolved != realFile {
		t.Errorf("got %q, want %q", resolved, realFile)
	}

	// Write: symlink within workspace should be allowed
	resolved, err = ValidatePath("good-link", ws, true)
	if err != nil {
		t.Fatalf("unexpected error for internal symlink (write): %v", err)
	}
	if resolved != realFile {
		t.Errorf("got %q, want %q", resolved, realFile)
	}
}

func TestValidatePath_WriteNewFile(t *testing.T) {
	ws, cleanup := setupTestWorkspace(t)
	defer cleanup()

	// Writing to a new file inside workspace should succeed
	resolved, err := ValidatePath("new-file.txt", ws, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(ws, "new-file.txt")
	if resolved != expected {
		t.Errorf("got %q, want %q", resolved, expected)
	}
}

func TestValidatePath_WriteNewFileInSubdir(t *testing.T) {
	ws, cleanup := setupTestWorkspace(t)
	defer cleanup()

	// Create a subdirectory
	subdir := filepath.Join(ws, "sub")
	os.MkdirAll(subdir, 0o755)

	resolved, err := ValidatePath("sub/new-file.txt", ws, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(ws, "sub", "new-file.txt")
	if resolved != expected {
		t.Errorf("got %q, want %q", resolved, expected)
	}
}

func TestValidatePath_WorkspaceBoundaryPrefix(t *testing.T) {
	// Ensure /workspace-evil does not match /workspace
	ws, cleanup := setupTestWorkspace(t)
	defer cleanup()

	// Create a sibling directory with a similar name prefix
	evilDir := filepath.Dir(ws) + "/" + filepath.Base(ws) + "-evil"
	os.MkdirAll(evilDir, 0o755)
	defer os.RemoveAll(evilDir)

	os.WriteFile(filepath.Join(evilDir, "data.txt"), []byte("x"), 0o644)

	_, err := ValidatePath(filepath.Join(evilDir, "data.txt"), ws, false)
	if err == nil {
		t.Fatal("expected error for path in sibling directory with similar prefix, got nil")
	}
}

func TestValidatePath_SymlinkDirEscape(t *testing.T) {
	ws, cleanup := setupTestWorkspace(t)
	defer cleanup()

	// Create a directory outside
	outsideDir, err := os.MkdirTemp("", "sandbox-outside-*")
	if err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	defer os.RemoveAll(outsideDir)

	// Create a symlink directory inside workspace pointing outside
	linkDir := filepath.Join(ws, "link-dir")
	os.Symlink(outsideDir, linkDir)

	_, err = ValidatePath("link-dir/file.txt", ws, true)
	if err == nil {
		t.Fatal("expected error for file under symlinked directory pointing outside, got nil")
	}
}

func TestValidateWorkingDir(t *testing.T) {
	ws, cleanup := setupTestWorkspace(t)
	defer cleanup()

	subdir := filepath.Join(ws, "work")
	os.MkdirAll(subdir, 0o755)

	resolved, err := ValidateWorkingDir("work", ws)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != subdir {
		t.Errorf("got %q, want %q", resolved, subdir)
	}

	// Outside workspace should fail
	_, err = ValidateWorkingDir("/tmp", ws)
	if err == nil {
		t.Fatal("expected error for working directory outside workspace, got nil")
	}
}
