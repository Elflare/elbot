package toolrun

import (
	"context"
	"path/filepath"
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
			entries, err := (logging.Reader{Dir: manager.LogDir()}).Query(context.Background(), logging.LogQuery{
				Prefix: "audit", Fields: map[string]string{"event": eventName},
			})
			if err != nil || len(entries) != 1 {
				t.Fatalf("event=%s entries=%+v error=%v", eventName, entries, err)
			}
			fields := entries[0].Fields
			if fields["session_id"] != "session" || fields[targetKey] != "missing" || fields["reason"] != "not_found_or_not_allowed" || fields["result_status"] != "skipped" {
				t.Fatalf("lost skip facts: %+v", fields)
			}
		})
	}
}
