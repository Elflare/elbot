package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestEntryProcess(t *testing.T) {
	if os.Getenv("ELBOT_ENTRY_TEST") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"elbot"}, os.Args[i+1:]...)
			main()
			return
		}
	}
	t.Fatal("missing process arguments")
}

func entryConfig(t *testing.T, platform string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"app.toml": fmt.Sprintf(`[storage]
sessions_sqlite_path = %q
chat_history_sqlite_path = %q
[sandbox]
root = %q
[file_delivery]
backend = "base64"
`, filepath.Join(root, "sessions.db"), filepath.Join(root, "history.db"), filepath.Join(root, "sandbox")) + platform,
		"providers.toml": "[providers.chat]\nbase_url = 'https://unused.invalid/v1'\napi_mode = 'chat'\nmodels = ['m']\n",
		"state.toml":     "[mode_models.work]\nprovider = 'chat'\nmodel = 'm'\n[mode_models.chat]\nprovider = 'chat'\nmodel = 'm'\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "app.toml")
}

func startEntry(t *testing.T, args ...string) (*exec.Cmd, <-chan error, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestEntryProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "ELBOT_ENTRY_TEST=1")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	return cmd, done, &output
}

func TestEntryCommandDispatch(t *testing.T) {
	path := entryConfig(t, "")
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"default falls back", nil, "already in use"},
		{"run starts locally", []string{"run"}, "already in use"},
		{"service starts locally", []string{"service", "run"}, "already in use"},
		{"client never falls back", []string{"client"}, "cli client is not configured"},
		{"named default never falls back", []string{"-c", "local"}, "cli client is not configured"},
		{"old command rejected", []string{"cli"}, "unknown command: cli"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, done, output := startEntry(t, append(tt.args, "--config", path)...)
			err := <-done
			if err == nil || !strings.Contains(output.String(), tt.want) {
				t.Fatalf("exit=%v output=%s; want %q", err, output, tt.want)
			}
		})
	}
}

func TestEntryClientCancellationDoesNotStartLocal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt is not supported on Windows")
	}
	for _, args := range [][]string{nil, {"client"}} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			requested := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested <- struct{}{}
				<-r.Context().Done()
			}))
			defer server.Close()
			t.Setenv("ELBOT_ENTRY_TOKEN", "test-token")
			path := entryConfig(t, fmt.Sprintf(`[platform.cli.clients.local]
url = %q
token_env = ["ELBOT_ENTRY_TOKEN"]
`, "ws"+strings.TrimPrefix(server.URL, "http")+"/cli/v1/ws"))
			cmd, done, output := startEntry(t, append(args, "--config", path)...)
			select {
			case <-requested:
			case err := <-done:
				t.Fatalf("client exited before connecting: %v: %s", err, output)
			}
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("cancel exit=%v output=%s", err, output)
			}
			if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
				t.Fatalf("client cancellation reached local startup: lock stat=%v", err)
			}
		})
	}
}

func TestEntryServiceStartsStopsAndReleasesLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt is not supported on Windows")
	}
	path := entryConfig(t, "[platform.cli]\nenabled = false\n")
	cmd, done, output := startEntry(t, "service", "run", "--config", path)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for ready := false; !ready; {
		select {
		case err := <-done:
			t.Fatalf("service exited before startup: %v: %s", err, output)
		case <-ticker.C:
			paths, err := filepath.Glob(filepath.Join(filepath.Dir(path), "logs", "elbot-*.log"))
			if err != nil {
				t.Fatal(err)
			}
			for _, logPath := range paths {
				data, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatal(err)
				}
				ready = ready || bytes.Contains(data, []byte("elbot_started"))
			}
		}
	}
	lock := flock.New(path + ".lock")
	defer lock.Close()
	if locked, err := lock.TryLock(); err != nil || locked {
		t.Fatalf("running service lock: locked=%v err=%v", locked, err)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("service stop=%v output=%s", err, output)
	}
	if locked, err := lock.TryLock(); err != nil || !locked {
		t.Fatalf("stopped service lock: locked=%v err=%v", locked, err)
	}
	for _, name := range []string{"sessions.db", "history.db"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), name)); err != nil {
			t.Fatal(err)
		}
	}
}
