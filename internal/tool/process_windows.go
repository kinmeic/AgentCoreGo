//go:build windows

package tool

import (
	"os/exec"
	"strconv"
)

func PrepareProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return killProcessTree(cmd) }
}

// KillProcess terminates a process and its descendants.
func KillProcess(cmd *exec.Cmd) error { return killProcessTree(cmd) }

func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	if err := exec.Command("taskkill", "/T", "/F", "/PID", pid).Run(); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
