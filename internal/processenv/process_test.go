package processenv

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestProcessTreeHelper(t *testing.T) {
	if os.Getenv("ELBOT_PROCESS_TREE_HELPER") != "1" {
		return
	}
	args := os.Args[len(os.Args)-3:]
	if args[0] == "parent" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$", "--", "child", args[1], args[2])
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		_ = cmd.Wait()
		os.Exit(0)
	}
	if err := os.WriteFile(args[1], []byte("ready"), 0600); err != nil {
		os.Exit(2)
	}
	time.Sleep(time.Second)
	if err := os.WriteFile(args[2], []byte("side effect"), 0600); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestCommandContextCancelsDescendants(t *testing.T) {
	t.Setenv("ELBOT_PROCESS_TREE_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"cancel", "timeout", "uv"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "timeout" {
				var timeoutCancel context.CancelFunc
				ctx, timeoutCancel = context.WithTimeout(ctx, 500*time.Millisecond)
				defer timeoutCancel()
			}
			dir := t.TempDir()
			ready, marker := filepath.Join(dir, "ready"), filepath.Join(dir, "sent")
			cmd := (Environment{}).CommandContext(ctx, executable, "-test.run=^TestProcessTreeHelper$", "--", "parent", ready, marker)
			if mode == "uv" {
				uv, err := exec.LookPath("uv")
				if err != nil {
					t.Skip("uv is not installed")
				}
				cmd = (Environment{}).CommandContext(ctx, uv, "--no-cache", "run", "--offline", "--no-project", "--no-sync", "--", executable, "-test.run=^TestProcessTreeHelper$", "--", "parent", ready, marker)
			}
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			finished := make(chan error, 1)
			go func() { finished <- cmd.Run() }()
			deadline := time.After(5 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				select {
				case err := <-finished:
					t.Fatalf("helper exited before ready: %v; %s", err, &output)
				case <-deadline:
					t.Fatal("helper did not become ready")
				case <-time.After(time.Millisecond):
				}
			}
			if mode != "timeout" {
				cancel()
			}
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("canceled process succeeded")
				}
			case <-deadline:
				t.Fatal("canceled command did not finish")
			}
			time.Sleep(1100 * time.Millisecond)
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("descendant performed side effect after cancellation: %v", err)
			}
		})
	}
}

func TestKillProcessTreeFailureDoesNotKillParent(t *testing.T) {
	t.Setenv("ELBOT_PROCESS_TREE_HELPER", "1")
	if runtime.GOOS == "windows" {
		// Make taskkill unavailable, so tree termination fails deterministically.
		t.Setenv("PATH", t.TempDir())
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ready, marker := filepath.Join(dir, "ready"), filepath.Join(dir, "alive")
	// On Unix this command inherits our process group, so a group with its PID
	// does not exist. On Windows the missing taskkill supplies the failure.
	cmd := exec.Command(executable, "-test.run=^TestProcessTreeHelper$", "--", "child", ready, marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait(); close(finished) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-finished })
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("helper exited before ready: %v", err)
		case <-deadline:
			t.Fatal("helper did not become ready")
		case <-time.After(time.Millisecond):
		}
	}
	if err := KillProcessTree(cmd); err == nil {
		t.Fatal("tree termination failure was hidden by killing the parent")
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("parent was killed after tree termination failed: %v", err)
		}
	case <-deadline:
		t.Fatal("parent did not finish normally")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("parent did not remain alive after termination failed: %v", err)
	}
}
