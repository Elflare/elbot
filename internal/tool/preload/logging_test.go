package preload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/security"
	"elbot/internal/signal"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

func TestPreloadAuditIdentityAndResult(t *testing.T) {
	for _, tc := range []struct {
		name, event, content string
		err                  error
		status               globalevents.ResultStatus
		level                slog.Level
	}{
		{"failed", "skill_preload_failed", "tool=guide", errors.New("detail failed"), globalevents.ResultFailed, slog.LevelError},
		{"canceled", "skill_preload_failed", "tool=guide", context.Canceled, globalevents.ResultCanceled, slog.LevelInfo},
		{"wrapper", "skill_wrapper_preload_skipped", "tool=missing reason=not_found_or_not_allowed", nil, globalevents.ResultSkipped, slog.LevelInfo},
		{"background", "background_preload_skipped", "name=guide reason=not_authorized", nil, globalevents.ResultSkipped, slog.LevelInfo},
	} {
		for _, withIdentity := range []bool{false, true} {
			for _, sessionID := range []string{"", "explicit"} {
				t.Run(fmt.Sprintf("%s/identity=%t/session=%s", tc.name, withIdentity, sessionID), func(t *testing.T) {
					ctx := t.Context()
					want := globalevents.LogIdentity{SessionID: &sessionID}
					if withIdentity {
						facts := contextinfo.Execution{SessionID: "context-session", RunID: "run", Attempt: "attempt", RequestID: "request", RootRequestID: "root"}
						ctx = contextinfo.WithExecution(ctx, facts)
						want.RunID, want.Attempt, want.RequestID, want.RootRequestID = &facts.RunID, &facts.Attempt, &facts.RequestID, &facts.RootRequestID
					}
					r := registry.NewRegistry(security.DefaultPermission())
					skill := &preloadSkill{availabilityTestTool: availabilityTestTool{name: "guide"}, err: tc.err}
					if tc.name == "wrapper" {
						skill.activate = []string{" missing ", "", "discover_tool", "missing"}
					}
					if err := r.Register(skill); err != nil {
						t.Fatal(err)
					}
					var records []globalevents.LogRecord
					connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error {
						if record.Module == "tool" {
							records = append(records, record)
						}
						return nil
					}, signal.ConnectOptions{})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(connection.Disconnect)
					s := newTestPreloader(PreloadOptions{Registry: r})
					if tc.name == "background" {
						_, err = s.PrepareBackground(ctx, share.BackgroundPreloadRequest{SessionID: sessionID, Names: []string{"guide"}, AllowedToolNames: []string{}})
					} else {
						_, err = s.PrepareSkills(ctx, sessionID, []string{"guide"})
					}
					if !errors.Is(err, tc.err) || len(records) != 1 {
						t.Fatalf("err=%v records=%+v", err, records)
					}
					record := records[0]
					if !reflect.DeepEqual(record.Identity, want) || record.EventName != tc.event || record.Level != tc.level || record.ResultStatus != tc.status || record.Category != globalevents.LogAudit || !strings.Contains(record.Content, tc.content) {
						t.Fatalf("unexpected audit: %+v identity=%+v want=%+v", record, record.Identity, want)
					}
				})
			}
		}
	}
}
