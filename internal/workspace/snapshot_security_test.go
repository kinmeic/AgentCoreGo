package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreDoesNotFollowReplacementSymlinks(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "directory"}[directory], func(t *testing.T) {
			service, root := newTestService(t)
			path := filepath.Join(root, "nested", "a.txt")
			writeTestFile(t, path, "original")
			snapshot, err := service.CreateSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			target := filepath.Join(outside, "a.txt")
			writeTestFile(t, target, "outside")
			link, destination := path, target
			if directory {
				link, destination = filepath.Dir(path), outside
			}
			if err := os.RemoveAll(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(destination, link); err != nil {
				t.Fatal(err)
			}
			if _, err := service.RestoreSnapshot(context.Background(), snapshot.ID, RestoreOptions{AllowChanges: true}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "outside" {
				t.Fatalf("outside file changed: %q %v", data, err)
			}
			data, err = os.ReadFile(path)
			if err != nil || string(data) != "original" {
				t.Fatalf("restore failed: %q %v", data, err)
			}
		})
	}
}

func TestRestorePreservesIgnoredFiles(t *testing.T) {
	root := t.TempDir()
	service, err := New(Config{Root: root, StateDir: filepath.Join(t.TempDir(), "state"), Ignore: []string{"cache/", "*.local"}})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "tracked"), "original")
	snapshot, err := service.CreateSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"cache/data", "config.local", ".git/config"} {
		writeTestFile(t, filepath.Join(root, path), "keep")
	}
	if _, err := service.RestoreSnapshot(context.Background(), snapshot.ID, RestoreOptions{AllowChanges: true}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"cache/data", "config.local", ".git/config"} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(data) != "keep" {
			t.Errorf("protected file %s lost: %q %v", path, data, err)
		}
	}
}

func TestRestoreRejectsCorruptBlobBeforeMutating(t *testing.T) {
	service, root := newTestService(t)
	path := filepath.Join(root, "a.txt")
	writeTestFile(t, path, "original")
	snapshot, err := service.CreateSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	digest := snapshot.Files[0].SHA256
	writeTestFile(t, filepath.Join(service.stateDir, "blobs", digest[:2], digest), "corrupt!")
	writeTestFile(t, path, "current")
	writeTestFile(t, filepath.Join(root, "new.txt"), "keep")
	if _, err := service.RestoreSnapshot(context.Background(), snapshot.ID, RestoreOptions{AllowChanges: true}); err == nil {
		t.Fatal("corrupt snapshot restored")
	}
	for name, want := range map[string]string{"a.txt": "current", "new.txt": "keep"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Errorf("workspace mutated: %s = %q, %v", name, data, err)
		}
	}
}

func TestSnapshotRejectsMalformedManifests(t *testing.T) {
	for _, files := range [][]SnapshotFile{
		{{Path: "../escape", Type: EntryDirectory}},
		{{Path: ".git/config", Type: EntryFile}},
		{{Path: "file", Type: EntryFile, SHA256: "x"}},
		{{Path: "link", Type: EntrySymlink, LinkTarget: "/tmp"}, {Path: "link/file", Type: EntryDirectory}},
		{{Path: "duplicate", Type: EntryDirectory}, {Path: "duplicate", Type: EntryDirectory}},
	} {
		service, _ := newTestService(t)
		snapshot, err := service.CreateSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Files = files
		snapshot.Digest = manifestDigest(snapshot)
		if err := service.writeManifest(snapshot); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Snapshot(snapshot.ID); err == nil {
			t.Fatalf("malformed manifest accepted: %#v", files)
		}
	}
}

func TestRestoreTracksMissingFilesLinksDirectoriesAndModes(t *testing.T) {
	for _, change := range []string{"delete-file", "change-link", "new-directory", "change-mode"} {
		t.Run(change, func(t *testing.T) {
			service, root := newTestService(t)
			writeTestFile(t, filepath.Join(root, "a.txt"), "original")
			if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
			snapshot, err := service.CreateSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "delete-file":
				err = os.Remove(filepath.Join(root, "a.txt"))
			case "change-link":
				if err = os.Remove(filepath.Join(root, "link")); err == nil {
					err = os.Symlink("elsewhere", filepath.Join(root, "link"))
				}
			case "new-directory":
				err = os.Mkdir(filepath.Join(root, "empty"), 0o755)
			case "change-mode":
				err = os.Chmod(filepath.Join(root, "a.txt"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.RestoreSnapshot(context.Background(), snapshot.ID, RestoreOptions{}); CodeOf(err) != CodeConflict {
				t.Fatalf("unapproved change was not rejected: %v", err)
			}
			if _, err := service.RestoreSnapshot(context.Background(), snapshot.ID, RestoreOptions{AllowChanges: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(root, "empty")); !os.IsNotExist(err) {
				t.Fatalf("new empty directory retained: %v", err)
			}
			info, err := os.Stat(filepath.Join(root, "a.txt"))
			if err != nil || info.Mode().Perm() != 0o640 {
				t.Fatalf("file permissions not restored: %v %v", info, err)
			}
		})
	}
}

func TestRestoreHandlesTypeChangesAndHardlinks(t *testing.T) {
	service, root := newTestService(t)
	writeTestFile(t, filepath.Join(root, "was-file"), "original")
	writeTestFile(t, filepath.Join(root, "was-dir", "child"), "child")
	snapshot, err := service.CreateSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "was-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "was-file"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "was-dir")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "was-dir"), "file")
	if _, err := service.RestoreSnapshot(context.Background(), snapshot.ID, RestoreOptions{AllowChanges: true}); err != nil {
		t.Fatal(err)
	}
	outsideLink := filepath.Join(t.TempDir(), "hardlink")
	if err := os.Link(filepath.Join(root, "was-file"), outsideLink); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "was-file"), "changed")
	if _, err := service.RestoreSnapshot(context.Background(), snapshot.ID, RestoreOptions{AllowChanges: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "was-file"))
	if err != nil || string(data) != "original" {
		t.Fatalf("file not restored: %q %v", data, err)
	}
	data, err = os.ReadFile(outsideLink)
	if err != nil || string(data) != "changed" {
		t.Fatalf("restore modified an external hardlink: %q %v", data, err)
	}
}
