package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidatePathRejectsSymlinkAncestorOfMissingDirectories(t *testing.T) {
	workspace, cleanup := setupTestWorkspace(t)
	defer cleanup()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePath("linked/new/nested/file.txt", workspace, true); err == nil {
		t.Fatal("expected symlink ancestor escape to be rejected")
	}
}

func TestSoulFileCannotEscapeWorkspace(t *testing.T) {
	workspace, cleanup := setupTestWorkspace(t)
	defer cleanup()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "SOUL.md"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "SOUL.md"), filepath.Join(workspace, "SOUL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSoulFile(workspace, "SOUL.md"); err == nil {
		t.Fatal("expected external SOUL.md symlink to be rejected")
	}
}
