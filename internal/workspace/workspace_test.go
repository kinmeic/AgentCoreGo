package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "state")
	service, err := New(Config{Root: root, StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	return service, root
}

func TestSnapshotRestoreAndConflictApproval(t *testing.T) {
	service, root := newTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "a.txt"), []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nested/a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.CreateSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Digest == "" || len(snapshot.Files) < 3 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "a.txt"), []byte("changed"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RestoreSnapshot(context.Background(), snapshot.ID, RestoreOptions{}); err == nil {
		t.Fatal("restore without change approval succeeded")
	}
	compensation, err := service.RestoreSnapshot(context.Background(), snapshot.ID, RestoreOptions{AllowChanges: true})
	if err != nil {
		t.Fatal(err)
	}
	if compensation.ID == snapshot.ID {
		t.Fatal("compensation snapshot was not created")
	}
	data, _ := os.ReadFile(filepath.Join(root, "nested", "a.txt"))
	if string(data) != "original" {
		t.Fatalf("restore content = %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("new file was not removed")
	}
	target, err := os.Readlink(filepath.Join(root, "link"))
	if err != nil || target != "nested/a.txt" {
		t.Fatalf("symlink not restored: %q %v", target, err)
	}
}

func TestWriteFileRequiresRevision(t *testing.T) {
	service, root := newTestService(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.WriteFile(context.Background(), "a.txt", []byte("two"), WriteOptions{}); err == nil {
		t.Fatal("write without revision succeeded")
	}
	page, err := service.ReadFile(context.Background(), "a.txt", ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.WriteFile(context.Background(), "a.txt", []byte("two"), WriteOptions{ExpectedRevision: page.Revision}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "two" {
		t.Fatalf("write content = %q", data)
	}
}

func TestGitStatusDiffAndLog(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	service, root := newTestService(t)
	runGitTest(t, root, "init", "-q")
	runGitTest(t, root, "config", "user.email", "test@example.com")
	runGitTest(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", "a.txt")
	runGitTest(t, root, "commit", "-qm", "initial")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", "a.txt")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := service.GitStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Clean || len(status.Staged) == 0 || len(status.Unstaged) == 0 || len(status.Untracked) != 1 {
		t.Fatalf("unexpected status: %#v", status)
	}
	diff, err := service.GitDiff(context.Background(), "head", "", "a.txt")
	if err != nil || !strings.Contains(diff, "three") {
		t.Fatalf("unexpected diff: %q %v", diff, err)
	}
	commits, err := service.GitLog(context.Background(), "", 10, "")
	if err != nil || len(commits) != 1 || commits[0].Subject != "initial" {
		t.Fatalf("unexpected log: %#v %v", commits, err)
	}
}

func runGitTest(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
}
