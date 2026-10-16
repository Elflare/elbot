package processenv

import (
	"fmt"
	"os/exec"
)

// ConfigureCommand makes context cancellation terminate the command's process
// tree. It must be called before Start on a command created with a context.
func ConfigureCommand(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return KillProcessTree(cmd) }
}

// KillProcessTree terminates a started command and its descendants. Wait still
// belongs to the caller and joins process exit and stdout/stderr collection.
func KillProcessTree(cmd *exec.Cmd) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run()
}

// ExitCanceled reports native exit evidence for a termination requested by us.
func ExitCanceled(cmd *exec.Cmd, cancelRequested bool) bool {
	return cancelRequested && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1
}
