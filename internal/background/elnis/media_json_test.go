package elnis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/media"
)

func TestCancelledEnqueueCommitsTerminalState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, close := newTestService(t, nil)
	defer close()
	enableMedia(t, s)
	item, err := testMedia(s).importBytes(ctx, []byte("cancelled input"), media.Input{Name: "input.png"})
	if err != nil {
		t.Fatal(err)
	}
	s.SetLLMEnqueuer(func(ctx context.Context, event QueuedLLMEvent) error {
		cancel()
		return ctx.Err()
	})
	req := testRequest(ModeLLM)
	req.Segments = []Segment{{Kind: SegmentKindImage, URL: item}}
	if _, err := s.Handle(ctx, "secret", req); err == nil {
		t.Fatal("expected cancelled enqueue")
	}
	record, err := testRepo(s).snapshotByKey(context.Background(), req.Elwisp.Name, req.Source, req.ID)
	if err != nil || record.Status != StatusFailed {
		t.Fatalf("cancelled event not marked failed: %v %v", record, err)
	}
	if got := testRepo(s).creates[0].MediaIDs; len(got) != 1 || got[0] != item {
		t.Fatalf("input references not requested: %v", got)
	}

}
func TestReportMediaJSONSources(t *testing.T) {
	ctx := context.Background()
	s, close := newTestService(t, nil)
	defer close()
	enableMedia(t, s)
	item, err := testMedia(s).importBytes(ctx, []byte("report"), media.Input{Name: "report.png"})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []llm.MessageSegment{
		{Type: llm.SegmentImage, URL: item},
		{Type: llm.SegmentFile, MediaID: item},
	} {
		got, err := s.importReportSegments(ctx, []llm.MessageSegment{source}, "report-json", t.TempDir())
		if err != nil || len(got) != 1 || got[0].MediaID != item || got[0].URL != "" {
			t.Fatalf("normalize: %v %v", got, err)
		}
		raw, err := json.Marshal(got)
		if err != nil || !strings.Contains(string(raw), `"media":"`+item+`"`) {
			t.Fatalf("persisted JSON: %s %v", raw, err)
		}
	}
	for _, source := range []llm.MessageSegment{
		{Type: llm.SegmentImage, MediaID: item, URL: item},
		{Type: llm.SegmentImage, MediaID: item, URL: "report.png"},
		{Type: llm.SegmentImage, URL: "media:bad"},
	} {
		if _, err := s.importReportSegments(ctx, []llm.MessageSegment{source}, "report-json", t.TempDir()); err == nil {
			t.Fatalf("accepted mixed or invalid source: %v", source)
		}
	}
}
