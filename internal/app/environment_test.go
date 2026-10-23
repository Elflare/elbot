package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func environmentConfigFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.toml")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func acquireEnvironmentLock(t *testing.T, path string, mode RunMode) string {
	t.Helper()
	resolved, lock, err := prepareEnvironment(path, mode)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	})
	return resolved
}

func requireEnvironmentBusy(t *testing.T, path string, mode RunMode) {
	t.Helper()
	_, lock, err := prepareEnvironment(path, mode)
	if lock != nil {
		_ = lock.Close()
		t.Fatal("busy config returned a lock")
	}
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("expected config contention, got %v", err)
	}
}

func TestEnvironmentConfigSelection(t *testing.T) {
	explicit := environmentConfigFixture(t)
	fromEnv := environmentConfigFixture(t)
	t.Setenv("ELBOT_CONFIG_FILE", fromEnv)
	for _, mode := range []RunMode{RunModeTUI, RunModeService} {
		t.Run(string(mode), func(t *testing.T) {
			if got := acquireEnvironmentLock(t, explicit, mode); got != explicit {
				t.Fatalf("explicit config = %q, want %q", got, explicit)
			}
			if got := acquireEnvironmentLock(t, "", mode); got != fromEnv {
				t.Fatalf("environment config = %q, want %q", got, fromEnv)
			}
		})
	}
}

func TestEnvironmentLockAliasesAndReplacement(t *testing.T) {
	path := environmentConfigFixture(t)
	acquireEnvironmentLock(t, path, RunModeTUI)
	t.Chdir(filepath.Dir(path))
	requireEnvironmentBusy(t, filepath.Base(path), RunModeService)
	t.Run("symlink", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "alias.toml")
		if err := os.Symlink(path, alias); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		requireEnvironmentBusy(t, alias, RunModeService)
	})
	updated := path + ".new"
	if err := os.WriteFile(updated, []byte("# updated config\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(updated, path); err != nil {
		t.Fatal(err)
	}
	requireEnvironmentBusy(t, path, RunModeService)
}

func TestEnvironmentFailures(t *testing.T) {
	path := environmentConfigFixture(t)
	if err := os.Mkdir(path+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		path string
		mode RunMode
		want string
	}{
		{"invalid mode", path, "invalid", "unknown run mode"},
		{"missing config", path + ".missing", RunModeTUI, "resolve config symlinks"},
		{"lock open failure", path, RunModeService, "acquire config lock"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, lock, err := prepareEnvironment(tt.path, tt.mode)
			if lock != nil {
				_ = lock.Close()
				t.Fatal("failed preparation returned a lock")
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEnvironmentLockProcessHelper(t *testing.T) {
	path := os.Getenv("ELBOT_TEST_CONFIG_LOCK_PATH")
	if path == "" {
		return
	}
	acquireEnvironmentLock(t, path, RunMode(os.Getenv("ELBOT_TEST_CONFIG_LOCK_MODE")))
	if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentCrossProcessLock(t *testing.T) {
	for _, owner := range []RunMode{RunModeTUI, RunModeService} {
		for _, contender := range []RunMode{RunModeTUI, RunModeService} {
			for _, crash := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/crash=%t", owner, contender, crash), func(t *testing.T) {
					path := environmentConfigFixture(t)
					executable, err := os.Executable()
					if err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, executable, "-test.run=^TestEnvironmentLockProcessHelper$")
					cmd.Env = append(os.Environ(), "ELBOT_TEST_CONFIG_LOCK_PATH="+path, "ELBOT_TEST_CONFIG_LOCK_MODE="+string(owner))
					cmd.Stderr = os.Stderr
					stdin, err := cmd.StdinPipe()
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = stdin.Close() })
					stdout, err := cmd.StdoutPipe()
					if err != nil {
						t.Fatal(err)
					}
					if err := cmd.Start(); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if cmd.ProcessState == nil {
							_ = cmd.Process.Kill()
							_ = cmd.Wait()
						}
					})
					if ready, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || ready != "locked\n" {
						t.Fatalf("child readiness = %q, error = %v", ready, err)
					}
					requireEnvironmentBusy(t, path, contender)
					acquireEnvironmentLock(t, environmentConfigFixture(t), contender)
					if crash {
						if err := cmd.Process.Kill(); err != nil {
							t.Fatal(err)
						}
					} else if err := stdin.Close(); err != nil {
						t.Fatal(err)
					}
					if err := cmd.Wait(); (err != nil) != crash {
						t.Fatalf("child exit error = %v, crash = %t", err, crash)
					}
					if _, err := os.Stat(path + ".lock"); err != nil {
						t.Fatalf("lock file should remain: %v", err)
					}
					acquireEnvironmentLock(t, path, contender)
				})
			}
		}
	}
}
