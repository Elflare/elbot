package processenv

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"testing/synctest"
	"time"
)

func TestProcessTreeHelper(t *testing.T) {
	if os.Getenv("ELBOT_PROCESS_TREE_HELPER") != "1" {
		return
	}
	args := os.Args[len(os.Args)-2:]
	if args[0] == "parent" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$", "--", "child", args[1])
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		_ = cmd.Wait()
		os.Exit(0)
	}
	conn, err := net.Dial("tcp", args[1])
	if err != nil {
		os.Exit(2)
	}
	if _, err := conn.Write([]byte("ready")); err != nil {
		os.Exit(2)
	}
	var command [1]byte
	if _, err := io.ReadFull(conn, command[:]); err != nil || command[0] != 'f' {
		os.Exit(2)
	}
	if _, err := conn.Write([]byte("alive")); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func newProcessTreeListener(t *testing.T) *net.TCPListener {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return listener
}

func acceptProcessTreeHelper(t *testing.T, listener *net.TCPListener) *net.TCPConn {
	t.Helper()
	conn, err := listener.AcceptTCP()
	if err != nil {
		t.Fatalf("helper did not connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var ready [5]byte
	if _, err := io.ReadFull(conn, ready[:]); err != nil || string(ready[:]) != "ready" {
		t.Fatalf("helper did not become ready: %q, %v", ready, err)
	}
	return conn
}

func TestCommandContextCancelsDescendants(t *testing.T) {
	t.Setenv("ELBOT_PROCESS_TREE_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"cancel", "timeout", "uv"} {
		t.Run(mode, func(t *testing.T) {
			var uv string
			if mode == "uv" {
				var err error
				uv, err = exec.LookPath("uv")
				if err != nil {
					t.Skip("uv is not installed")
				}
			}
			listener := newProcessTreeListener(t)
			ready := make(chan *net.TCPConn, 1)
			// This goroutine and its network watchdogs use real time. Receiving
			// its ready signal keeps the bubble clock still during process startup.
			go func() {
				defer close(ready)
				ready <- acceptProcessTreeHelper(t, listener)
			}()
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if mode == "timeout" {
					var timeoutCancel context.CancelFunc
					ctx, timeoutCancel = context.WithTimeout(ctx, 500*time.Millisecond)
					defer timeoutCancel()
				}
				address := listener.Addr().String()
				cmd := (Environment{}).CommandContext(ctx, executable, "-test.run=^TestProcessTreeHelper$", "--", "parent", address)
				if mode == "uv" {
					cmd = (Environment{}).CommandContext(ctx, uv, "--no-cache", "run", "--offline", "--no-project", "--no-sync", "--", executable, "-test.run=^TestProcessTreeHelper$", "--", "parent", address)
				}
				// Files avoid exec's output-copy goroutines waiting on real pipes:
				// those would prevent the virtual clock from advancing.
				output, err := os.CreateTemp(t.TempDir(), "process-output")
				if err != nil {
					t.Fatal(err)
				}
				defer output.Close()
				cmd.Stdout, cmd.Stderr = output, output
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if cmd.ProcessState == nil {
						_ = KillProcessTree(cmd)
						_ = cmd.Wait()
					}
				}()
				conn := <-ready
				if conn == nil {
					t.Fatal("helper did not become ready")
				}
				if mode == "timeout" {
					<-ctx.Done()
				} else {
					cancel()
				}
				if err := cmd.Wait(); err == nil {
					t.Fatal("canceled process succeeded")
				}
				// The child cannot close voluntarily without a finish command.
				var probe [1]byte
				n, err := conn.Read(probe[:])
				if n != 0 || err == nil {
					t.Fatalf("descendant survived cancellation: read %d bytes, error %v", n, err)
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					t.Fatalf("descendant connection remained open: %v", err)
				}
			})
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
	listener := newProcessTreeListener(t)
	// On Unix this command inherits our process group, so a group with its PID
	// does not exist. On Windows the missing taskkill supplies the failure.
	cmd := exec.Command(executable, "-test.run=^TestProcessTreeHelper$", "--", "child", listener.Addr().String())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait(); close(finished) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-finished })
	conn := acceptProcessTreeHelper(t, listener)
	if err := KillProcessTree(cmd); err == nil {
		t.Fatal("tree termination failure was hidden by killing the parent")
	}
	if _, err := conn.Write([]byte("f")); err != nil {
		t.Fatalf("helper was killed after tree termination failed: %v", err)
	}
	var alive [5]byte
	if _, err := io.ReadFull(conn, alive[:]); err != nil || string(alive[:]) != "alive" {
		t.Fatalf("helper did not remain alive: %q, %v", alive, err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("parent was killed after tree termination failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not finish normally")
	}
}
