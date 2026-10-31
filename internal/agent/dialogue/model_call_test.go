package dialogue

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"elbot/internal/events"
	"elbot/internal/modelmgr"
	"elbot/internal/signal"
)

func TestAssistantMessageLogSummary(t *testing.T) {
	boundary := strings.Repeat("文", 512)
	for _, tc := range []struct {
		name, text, raw, want, wantRaw string
	}{
		{"equal", "回复", "回复", "回复", ""},
		{"different", "回复", "原始回复", "回复", "原始回复"},
		{"empty", "", "", "", ""},
		{"boundary", boundary, boundary, boundary, ""},
		{"over_boundary", boundary + "文", boundary + "文", strings.Repeat("文", 511) + "…", ""},
		{"unicode", strings.Repeat("文😀", 300), strings.Repeat("文😀", 300), string([]rune(strings.Repeat("文😀", 300))[:511]) + "…", ""},
		{"late_difference", "回复", "回复" + strings.Repeat("文", 512) + "异", "回复", string([]rune("回复" + strings.Repeat("文", 512))[:511]) + "…"},
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
			var content struct {
				Provider      string  `json:"provider"`
				Model         string  `json:"model"`
				Text          string  `json:"text"`
				RawText       *string `json:"raw_text"`
				ElapsedMS     int64   `json:"elapsed_ms"`
				ToolCallCount int     `json:"tool_call_count"`
			}
			if err := json.Unmarshal([]byte(records[0].Content), &content); err != nil {
				t.Fatal(err)
			}
			if content.Text != tc.want || utf8.RuneCountInString(content.Text) > 512 || !utf8.ValidString(content.Text) {
				t.Fatalf("source text=%q, want=%q", content.Text, tc.want)
			}
			if tc.raw == tc.text && content.RawText != nil || tc.raw != tc.text && (content.RawText == nil || *content.RawText != tc.wantRaw) {
				t.Fatalf("source raw text=%v, want=%q", content.RawText, tc.wantRaw)
			}
			if records[0].Type != events.ContentJSON || content.Provider != "p" || content.Model != "m" || content.ElapsedMS != 0 || content.ToolCallCount != 0 {
				t.Fatalf("source metadata changed: %s", records[0].Content)
			}
			if completed.Text != tc.text || completed.SourceText != tc.raw || observation.Event().Text != tc.text || observation.Event().SourceText != tc.raw {
				t.Fatal("logging changed business text")
			}
		})
	}
}
