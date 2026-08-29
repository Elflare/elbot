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
	out, err := exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).CombinedOutput()
	fmt.Printf("taskkill pid=%d err=%v out=%s\n", cmd.Process.Pid, err, out)
	_ = cmd.Process.Kill()
}
