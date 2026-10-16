//go:build !windows

package processenv

import (
	"os/exec"
	"syscall"
)

// ConfigureCommand gives an unstarted command its own process group and makes
// context cancellation terminate that group, including inherited descendants.
func ConfigureCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error { return KillProcessTree(cmd) }
}

// KillProcessTree terminates a started command's process group. The caller must
// still call Wait to join process exit and stdout/stderr collection.
func KillProcessTree(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// ExitCanceled reports native exit evidence for a termination requested by us.
func ExitCanceled(cmd *exec.Cmd, cancelRequested bool) bool {
	if !cancelRequested || cmd.ProcessState == nil {
		return false
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL
}
