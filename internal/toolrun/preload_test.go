package toolrun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/logging"
	"elbot/internal/tool"
)

func TestPreloadWrapperSkipAuditUsesEventName(t *testing.T) {
	for _, background := range []bool{false, true} {
		eventName, targetKey := "skill_wrapper_preload_skipped", "tool"
		if background {
			eventName, targetKey = "background_preload_skipped", "name"
		}
		t.Run(eventName, func(t *testing.T) {
			manager, err := logging.NewManager("error", filepath.Join(t.TempDir(), "sessions.db"), 30)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Close(context.Background()) })
			preloader := NewPreloadService(PreloadOptions{Registry: tool.NewRegistry()})
			if got := preloader.preloadWrapper(context.Background(), "session", "missing", background); len(got) != 0 {
				t.Fatalf("unexpected tools: %+v", got)
			}
			if err := manager.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(manager.LogDir(), "audit-*.log"))
			if err != nil || len(files) != 1 {
				t.Fatalf("files=%v err=%v", files, err)
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]string
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			if fields["event"] != eventName || fields["session_id"] != "session" || !strings.Contains(fields["content"], targetKey+"=missing") || !strings.Contains(fields["content"], "reason=not_found_or_not_allowed") || fields["result_status"] != "skipped" {
				t.Fatalf("lost skip facts: %+v", fields)
			}
		})
	}
}
