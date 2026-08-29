//go:build windows

package rules

import (
	"fmt"
	"os/exec"
)

func configureExecHookProcess(cmd *exec.Cmd) {}

func killExecHookProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run()
	_ = cmd.Process.Kill()
}
