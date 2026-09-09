package elnis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/media"
)

func TestCancelledEnqueueReleasesMediaReference(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, close := newTestService(t, nil)
	defer close()
	enableMedia(t, s)
	item, err := s.media.ImportBytes(ctx, []byte("cancelled input"), media.Input{Name: "input.png"})
	if err != nil {
		t.Fatal(err)
	}
	s.SetLLMEnqueuer(func(ctx context.Context, event QueuedLLMEvent) error {
		cancel()
		return ctx.Err()
	})
	req := testRequest(ModeLLM)
	req.Segments = []Segment{{Kind: SegmentKindImage, URL: item.ID}}
	if _, err := s.Handle(ctx, "secret", req); err == nil {
		t.Fatal("expected cancelled enqueue")
	}
	record, err := s.store.ElnisEvents().GetByKey(context.Background(), req.Elwisp.Name, req.Source, req.ID)
	if err != nil || record.Status != StatusFailed {
		t.Fatalf("cancelled event not marked failed: %v %v", record, err)
	}
	refs, err := s.store.MediaReferences().ListMediaIDs(context.Background(), item.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("cancelled event leaked references: %v %v", refs, err)
	}
}
func TestReportMediaJSONSources(t *testing.T) {
	ctx := context.Background()
	s, close := newTestService(t, nil)
	defer close()
	enableMedia(t, s)
	item, err := s.media.ImportBytes(ctx, []byte("report"), media.Input{Name: "report.png"})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []llm.MessageSegment{
		{Type: llm.SegmentImage, URL: item.ID},
		{Type: llm.SegmentFile, MediaID: item.ID},
	} {
		got, err := s.importReportSegments(ctx, []llm.MessageSegment{source}, "report-json", t.TempDir())
		if err != nil || len(got) != 1 || got[0].MediaID != item.ID || got[0].URL != "" {
			t.Fatalf("normalize: %v %v", got, err)
		}
		raw, err := json.Marshal(got)
		if err != nil || !strings.Contains(string(raw), `"media":"`+item.ID+`"`) || strings.Contains(string(raw), `"media_id"`) {
			t.Fatalf("persisted JSON: %s %v", raw, err)
		}
	}
	for _, source := range []llm.MessageSegment{
		{Type: llm.SegmentImage, MediaID: item.ID, URL: item.ID},
		{Type: llm.SegmentImage, MediaID: item.ID, URL: "report.png"},
		{Type: llm.SegmentImage, URL: "media:bad"},
	} {
		if _, err := s.importReportSegments(ctx, []llm.MessageSegment{source}, "report-json", t.TempDir()); err == nil {
			t.Fatalf("accepted mixed or invalid source: %v", source)
		}
	}
}
