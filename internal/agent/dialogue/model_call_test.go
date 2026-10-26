package dialogue

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"strings"
	"testing"
	"unicode/utf8"

	"elbot/internal/events"
	"elbot/internal/modelmgr"
	"elbot/internal/signal"
)

func TestAssistantMessageLogSummary(t *testing.T) {
	const header = "provider=p\nmodel=m\nelapsed_ms=0\ntext="
	const footer = "\ntool_call_count=0"
	boundary := strings.Repeat("文", 512-utf8.RuneCountInString(header+footer))
	for _, tc := range []struct {
		name, text, raw, want string
	}{
		{"equal", "回复", "回复", header + "回复" + footer},
		{"different", "回复", "原始回复", header + "回复\nraw_text=原始回复" + footer},
		{"empty", "", "", header + footer},
		{"boundary", boundary, boundary, header + boundary + footer},
		{"over_boundary", boundary + "文", boundary + "文", header + boundary + "文\ntool_call_count…"},
		{"unicode", strings.Repeat("文😀", 300), strings.Repeat("文😀", 300), string([]rune(header + strings.Repeat("文😀", 300))[:511]) + "…"},
		{"late_difference", "回复", "回复" + strings.Repeat("文", 512) + "异", string([]rune(header + "回复\nraw_text=回复" + strings.Repeat("文", 512))[:511]) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var records []events.LogRecord
			logs, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "assistant_message" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(logs.Disconnect)
			processor := &CallProcessor{Completed: signal.New[agentshare.ModelCallCompletedEvent, signal.Empty]("test.completed")}
			var completed agentshare.ModelCallCompletedEvent
			connection, err := processor.Completed.Observe(func(_ context.Context, event agentshare.ModelCallCompletedEvent) error {
				completed = event
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Disconnect)
			observation := processor.Begin("session", modelmgr.Selection{})
			observation.Event().Provider, observation.Event().Model = "p", "m"
			observation.Event().OutputReady = true
			observation.Event().ElapsedMS = 0
			observation.Event().Text, observation.Event().SourceText = tc.text, tc.raw
			observation.Publish(t.Context(), nil)
			if len(records) != 1 {
				t.Fatalf("assistant records=%d", len(records))
			}
			if got := records[0].Content; got != tc.want || utf8.RuneCountInString(got) > 512 || !utf8.ValidString(got) {
				t.Fatalf("source log=%q, want=%q", got, tc.want)
			}
			if records[0].ContentLimit != 4096 {
				t.Fatalf("center limit changed: %d", records[0].ContentLimit)
			}
			if completed.Text != tc.text || completed.SourceText != tc.raw || observation.Event().Text != tc.text || observation.Event().SourceText != tc.raw {
				t.Fatal("logging changed business text")
			}
		})
	}
}
