//go:build !windows

package builtin

import (
	"agent-core/internal/tool"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTerminalKillByConversationKillsProcessGroup(t *testing.T) {
	workspace := testWorkspace(t)
	registry := tool.NewProcessRegistry()
	terminal := NewTerminalTool(workspace, registry)
	ctx := tool.WithExecutionInfo(context.Background(), tool.ExecutionInfo{ConversationID: "conv"})
	result := make(chan error, 1)
	go func() {
		_, err := terminal.Execute(ctx, map[string]any{
			"command":         "sleep 30 & child=$!; printf '%s' \"$child\" > child.pid; wait \"$child\"",
			"timeout_seconds": float64(20),
		})
		result <- err
	}()
	pidPath := filepath.Join(workspace, "child.pid")
	var pid int
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		data, err := os.ReadFile(pidPath)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child process did not start")
	}
	processes := registry.List()
	if len(processes) != 1 || processes[0].ConvID != "conv" {
		t.Fatalf("process was not correlated: %#v", processes)
	}
	if killed := registry.KillByConversation("conv"); killed != 1 {
		t.Fatalf("killed %d processes, want 1", killed)
	}
	select {
	case err := <-result:
		if err == nil || errors.Is(err, context.Canceled) && ctx.Err() == nil {
			t.Fatalf("unexpected terminal result: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal did not stop")
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child process %d survived process-group cleanup", pid)
}
