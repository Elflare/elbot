//go:build windows

package builtin

import (
	"fmt"
	"os/exec"
)

func configureShellProcess(cmd *exec.Cmd) {}

func killShellProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run()
	_ = cmd.Process.Kill()
}
