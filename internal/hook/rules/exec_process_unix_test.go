//go:build !windows

package rules

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/signal"
)

func TestExecIndependentSignalHelper(t *testing.T) {
	if os.Getenv("ELBOT_EXEC_SIGNAL_HELPER") != "1" {
		return
	}
	if err := execHelperHandshake(); err != nil {
		os.Exit(2)
	}
	writeProtocolTestResult(map[string]any{"status": "completed"})
	// No newline: the source log flush runs after Wait has observed this exit.
	fmt.Fprint(os.Stderr, "independent signal exit")
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	os.Exit(2)
}

func TestExecIndependentSignalSurvivesLaterCancellation(t *testing.T) {
	t.Setenv("ELBOT_EXEC_SIGNAL_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	connection, err := events.LogSubmitted.Connect(func(_ context.Context, record events.LogRecord) error {
		if record.EventName == "hook_exec_stderr" && strings.Contains(record.Content, "independent signal exit") {
			cancel()
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	_, _, result, err := (Module{}).runExec(ctx, hook.Event{}, Action{Command: []string{executable, "-test.run=^TestExecIndependentSignalHelper$"}}, state{})
	if !errors.Is(err, context.Canceled) || result.ResultStatus != events.ResultFailed || !strings.Contains(result.Content, "signal: killed") || !strings.Contains(result.Content, "context canceled") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestExecExitCanceledChecksActualSignal(t *testing.T) {
	for _, tc := range []struct {
		script   string
		canceled bool
	}{
		{"kill -KILL $$", true},
		{"kill -TERM $$", false},
		{"exit 7", false},
	} {
		cmd := exec.Command("sh", "-c", tc.script)
		if err := cmd.Run(); err == nil {
			t.Fatal("expected process failure")
		}
		if got := execExitCanceled(cmd, true); got != tc.canceled {
			t.Fatalf("script=%s canceled=%t", tc.script, got)
		}
		if execExitCanceled(cmd, false) {
			t.Fatal("external termination became host cancellation")
		}
	}
}
