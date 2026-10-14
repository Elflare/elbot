//go:build windows

package rules

import (
	"fmt"
	"os/exec"
)

func configureExecHookProcess(cmd *exec.Cmd) {}

func killExecHookProcessTree(cmd *exec.Cmd) bool {
	if err := exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run(); err == nil {
		_ = cmd.Process.Kill()
		return true
	}
	return cmd.Process.Kill() == nil
}

// Process.Kill terminates a Windows process with exit code 1.
func execExitCanceled(cmd *exec.Cmd, cancelRequested bool) bool {
	return cancelRequested && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1
}
