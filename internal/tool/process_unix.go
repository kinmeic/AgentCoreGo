//go:build !windows

package tool

import (
	"os/exec"
	"syscall"
)

func PrepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessTree(cmd) }
}

// KillProcess terminates a process and its descendants.
func KillProcess(cmd *exec.Cmd) error { return killProcessTree(cmd) }

func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
