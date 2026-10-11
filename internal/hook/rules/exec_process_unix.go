//go:build !windows

package rules

import (
	"os/exec"
	"syscall"
)

func configureExecHookProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killExecHookProcessTree(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		_ = cmd.Process.Kill()
		return true
	}
	return cmd.Process.Kill() == nil
}

// execExitCanceled checks native exit facts for this invocation only.
func execExitCanceled(cmd *exec.Cmd, cancelRequested bool) bool {
	if !cancelRequested || cmd.ProcessState == nil {
		return false
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL
}
