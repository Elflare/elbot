package platform

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/signal"
	"elbot/internal/testutil"
)

func TestCLIFrontendUsesConfigDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "frontend", "index.html"), 0700); err != nil {
		t.Fatal(err)
	}
	logs := make(chan events.LogRecord, 1)
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) {
		if record.EventName == "cli_frontend_init_failed" && strings.Contains(record.Content, dir) {
			logs <- record
		}
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	cfg := &config.Config{
		ConfigPath: filepath.Join(dir, "app.toml"),
		Platform: map[string]map[string]any{
			"cli": {"enabled": true, "server": map[string]any{
				"enabled": true, "listen": "127.0.0.1:0",
				"tokens": map[string][]string{"user": {"ELBOT_CLI_TEST_TOKEN"}},
			}},
		},
	}
	m, err := NewSupervisor(t.Context(), cfg, SupervisorDeps{Mode: "service", Store: testutil.NewSQLiteStore(t)})
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, adapter := range m.adapters {
		if adapter.Name() != "cli" {
			continue
		}
		if err := adapter.Start(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		select {
		case record := <-logs:
			if record.Level != slog.LevelError || !strings.Contains(record.Content, "index.html is not a regular file") {
				t.Fatalf("unexpected initialization error: %+v", record)
			}
		case <-time.After(time.Second):
			t.Fatal("did not inspect frontend next to configuration")
		}
		return
	}
	t.Fatal("missing remote CLI")
}
