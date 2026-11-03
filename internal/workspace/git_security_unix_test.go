//go:build !windows

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceGitDoesNotExecuteRepositoryHelpers(t *testing.T) {
	service, root := newTestService(t)
	runGitTest(t, root, "init", "-q")
	writeTestFile(t, filepath.Join(root, "fsmonitor"), "#!/bin/sh\ntouch helper-ran\n")
	if err := os.Chmod(filepath.Join(root, "fsmonitor"), 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "config", "core.fsmonitor", "./fsmonitor")
	if _, err := service.GitStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "helper-ran")); !os.IsNotExist(err) {
		t.Fatalf("repository helper executed: %v", err)
	}
}
