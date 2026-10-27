//go:build linux

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"elbot/internal/delivery"
)

func TestLocalTerminalLifecycle(t *testing.T) {
	if os.Getenv("ELBOT_CLI_TERMINAL_HELPER") == "1" {
		a := New()
		if !a.interactive {
			t.Fatal("not a real terminal")
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := a.Start(ctx, nil); err != nil {
			t.Fatal(err)
		}
		cancel() // 应用停止只关接入，不能提前退出终端展示。
		a.BeginClose()
		deadline, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if err := a.Wait(deadline); err != nil {
			t.Fatal(err)
		}
		a.mu.Lock()
		program := a.program
		a.mu.Unlock()
		if program == nil {
			t.Fatal("terminal released before Close")
		}
		if _, _, err := a.SendChat(context.Background(), []delivery.Output{delivery.Text("draining")}); err != nil {
			t.Fatal(err)
		}
		if err := a.Close(deadline); err != nil {
			t.Fatal(err)
		}
		a.mu.Lock()
		program = a.program
		a.mu.Unlock()
		if program != nil {
			t.Fatal("terminal still running after Close")
		}
		if _, _, err := a.SendChat(context.Background(), []delivery.Output{delivery.Text("closed")}); err == nil {
			t.Fatal("send succeeded after Close")
		}
		fmt.Println("CLI_TERMINAL_CLOSED")
		return
	}
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		t.Fatal(errno)
	}
	var number uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&number))); errno != 0 {
		t.Fatal(errno)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	size := [4]uint16{24, 80}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&size))); errno != 0 {
		t.Fatal(errno)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLocalTerminalLifecycle$", "-test.count=1")
	cmd.Env = append(os.Environ(), "ELBOT_CLI_TERMINAL_HELPER=1", "TERM=xterm-256color")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	output := make(chan string, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	slave.Close()
	go func() { data, _ := io.ReadAll(master); output <- string(data) }()
	err = cmd.Wait()
	text := <-output
	if err != nil || !strings.Contains(text, "CLI_TERMINAL_CLOSED") {
		t.Fatalf("%v\n%s", err, text)
	}
}
