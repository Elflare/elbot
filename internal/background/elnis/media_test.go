package elnis

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportMediaSurvivesWorkspaceRemovalAndRetry(t *testing.T) {
	ctx := context.Background()
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"result","report_segments":[{"type":"image","url":"chart.png"}]}`}
	fail := true
	var sent []delivery.Output
	s, close := newTestServiceWithRunner(t, runner, func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		if fail {
			{
				err := errors.New("offline")
				logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
				if err != nil {
					logResult.ResultStatus = events.ResultFailed
					logResult.Content = err.Error()
					if err == context.Canceled {
						logResult.ResultStatus = events.ResultCanceled
					}
				}
				return delivery.Receipt{}, logResult, err
			}
		}
		sent = outputs
		return delivery.Receipt{PlatformMessageIDs: []string{"report-1"}}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	})
	defer close()
	enableMedia(t, s)
	workspace := filepath.Join(s.sandboxRoot, "elnis", "watcher")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "chart.png")
	if err := os.WriteFile(path, []byte("report image"), 0600); err != nil {
		t.Fatal(err)
	}
	var queued QueuedLLMEvent
	s.SetLLMEnqueuer(func(ctx context.Context, event QueuedLLMEvent) error { queued = event; return nil })
	req := testRequest(ModeLLM)
	req.Targets = []Target{{Platform: "qqonebot", Type: "group", ID: "123"}}
	if _, err := s.Handle(ctx, "secret", req); err != nil {
		t.Fatal(err)
	}
	if err := s.RunLLMEvent(ctx, queued.Event, queued.EventID); err == nil {
		t.Fatal("expected failed send")
	}
	deliveries, err := s.store.ListReportDeliveries(ctx, queued.EventID)
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("outbox %v %v", deliveries, err)
	}
	if strings.Contains(deliveries[1].Output, path) || !strings.Contains(deliveries[1].Output, `"media":"media:`) {
		t.Fatal(deliveries[1].Output)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fail = false
	if _, err := s.recoverReports(ctx, true); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatal(sent)
	}
	id := sent[1].Source.MediaID
	if data := testMedia(s).data[id]; string(data) != "report image" {
		t.Fatalf("lost media %q", data)
	}

}

func TestDataURIFilenameExtension(t *testing.T) {
	for _, name := range []string{"chart.png", "chart.PNG", "chart", ""} {
		t.Run(name, func(t *testing.T) {
			s, close := newTestService(t, nil)
			defer close()
			enableMedia(t, s)
			outputs, err := s.directMediaOutputs(context.Background(), Event{Request: Request{Segments: []Segment{{Kind: SegmentKindImage, URL: "data:image/png;base64,aGVsbG8=", Name: name}}}}, "test")
			if err != nil {
				t.Fatal(err)
			}
			got := outputs[0].Name
			if name != "" {
				want := name
				if filepath.Ext(want) == "" {
					want += ".png"
				}
				if got != want {
					t.Fatalf("filename = %q, want %q", got, want)
				}
			} else if filepath.Ext(got) != ".png" {
				t.Fatalf("filename %q", got)
			}
		})
	}
}
