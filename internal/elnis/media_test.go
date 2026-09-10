package elnis

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/storage"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func enableMedia(t *testing.T, s *Service) {
	t.Helper()
	root := t.TempDir()
	s.media = media.NewManager(s.store, root, &media.LocalBackend{Root: root})
}

func TestDirectMediaCenterAndNoEagerDownloads(t *testing.T) {
	ctx := context.Background()
	downloads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	defer server.Close()
	var sent []delivery.Output
	s, close := newTestService(t, func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
		sent = outputs
		return delivery.Receipt{PlatformMessageIDs: []string{"sent-1", "sent-1"}}, nil
	})
	defer close()
	enableMedia(t, s)
	for _, mode := range []string{ModeRecord, ModeLLM} {
		req := testRequest(mode)
		req.ID = mode
		req.Segments = []Segment{{Kind: SegmentKindImage, URL: server.URL, Name: "chart.png"}}
		if _, err := s.Handle(ctx, "secret", req); err != nil {
			t.Fatal(err)
		}
	}
	if downloads != 0 {
		t.Fatal("eager download")
	}
	req := testRequest(ModeDirect)
	req.Segments = []Segment{{Kind: SegmentKindImage, URL: server.URL, Name: "chart.png"}}
	req.Targets = []Target{{Platform: "qqonebot", Type: "group", ID: "123"}}
	if _, err := s.Handle(ctx, "secret", req); err != nil {
		t.Fatal(err)
	}
	if downloads != 1 || len(sent) != 1 || !media.ValidID(sent[0].Source.MediaID) || sent[0].Source.Path != "" || sent[0].Name != "chart.png" {
		t.Fatalf("direct %v downloads=%d", sent, downloads)
	}
	if _, err := os.Stat(s.sandboxRoot); !os.IsNotExist(err) {
		t.Fatalf("sandbox copy created %v", err)
	}
	id := sent[0].Source.MediaID
	if _, err := s.Handle(ctx, "secret", req); err != nil {
		t.Fatal(err)
	}
	if downloads != 1 {
		t.Fatal("duplicate downloads")
	}
	req.ID = "existing"
	req.Segments[0].URL = id
	if _, err := s.Handle(ctx, "secret", req); err != nil {
		t.Fatal(err)
	}
	if downloads != 1 {
		t.Fatal("ID downloaded")
	}
	for _, seg := range []Segment{{Kind: SegmentKindImage, URL: "media:bad"}, {Kind: SegmentKindImage, URL: id + server.URL}, {Kind: SegmentKindImage, URL: "C:/outside.png"}, {Kind: SegmentKindImage, URL: "media:" + strings.Repeat("f", 64)}} {
		req.ID = storage.NewID()
		req.Segments = []Segment{seg}
		if _, err := s.Handle(ctx, "secret", req); err == nil {
			t.Fatalf("accepted %v", seg)
		}
	}
	event := Event{Request: testRequest(ModeDirect), ResolvedTargets: "[]"}
	event.Request.Segments = []Segment{{Kind: SegmentKindImage, URL: server.URL}}
	// No-target direct must not fetch even if final event update fails for this synthetic event.
	_ = s.runDirect(ctx, event, "missing")
	if downloads != 1 {
		t.Fatal("no-target downloaded")
	}
}

func TestReportMediaSurvivesWorkspaceRemovalAndRetry(t *testing.T) {
	ctx := context.Background()
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"result","report_segments":[{"type":"image","url":"chart.png"}]}`}
	fail := true
	var sent []delivery.Output
	s, close := newTestServiceWithRunner(t, runner, func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
		if fail {
			return delivery.Receipt{}, errors.New("offline")
		}
		sent = outputs
		return delivery.Receipt{PlatformMessageIDs: []string{"report-1"}}, nil
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
	deliveries, err := s.store.ElnisEvents().ListReportDeliveries(ctx, queued.EventID)
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("outbox %v %v", deliveries, err)
	}
	if strings.Contains(deliveries[1].Output, path) || !strings.Contains(deliveries[1].Output, `"media":"media:`) || strings.Contains(deliveries[1].Output, `"MediaID"`) {
		t.Fatal(deliveries[1].Output)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fail = false
	if err := s.recoverReports(ctx, true); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatal(sent)
	}
	id := sent[1].Source.MediaID
	if data, _, err := s.media.Read(ctx, id); err != nil || string(data) != "report image" {
		t.Fatalf("lost media %q %v", data, err)
	}
	refs, err := s.store.MediaReferences().ListMediaIDs(ctx, id)
	if err != nil || len(refs) != 0 {
		t.Fatalf("report refs %v %v", refs, err)
	}
	if issues, err := s.store.Media().CheckReferences(ctx); err != nil || len(issues) != 0 {
		t.Fatalf("audit %v %v", issues, err)
	}
}

func TestQueuedMediaReferencesRecoverAfterInterruption(t *testing.T) {
	ctx := context.Background()
	s, close := newTestService(t, nil)
	defer close()
	enableMedia(t, s)
	item, err := s.media.ImportBytes(ctx, []byte("queued"), media.Input{Name: "input.png"})
	if err != nil {
		t.Fatal(err)
	}
	req := testRequest(ModeLLM)
	req.Segments = []Segment{{Kind: SegmentKindImage, URL: item.ID}}
	if _, err := s.Handle(ctx, "secret", req); err != nil {
		t.Fatal(err)
	}
	refs, err := s.store.MediaReferences().ListMediaIDs(ctx, item.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("queue refs %v %v", refs, err)
	}
	if err := s.store.Media().RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	refs, err = s.store.MediaReferences().ListMediaIDs(ctx, item.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("interrupted refs %v %v", refs, err)
	}
}

func TestDirectPartialDeliveryReleasesEventReferences(t *testing.T) {
	ctx := context.Background()
	var sentID string
	s, close := newTestService(t, func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
		if target.GroupID == "2" {
			return delivery.Receipt{}, errors.New("offline")
		}
		sentID = outputs[0].Source.MediaID
		return delivery.Receipt{PlatformMessageIDs: []string{"success"}}, nil
	})
	defer close()
	enableMedia(t, s)
	req := testRequest(ModeDirect)
	req.Targets = []Target{{Platform: "qqonebot", Type: "group", ID: "1"}, {Platform: "qqonebot", Type: "group", ID: "2"}}
	req.Segments = []Segment{{Kind: SegmentKindImage, URL: "data:image/png;base64,cGFydGlhbA==", Name: "partial.png"}}
	if _, err := s.Handle(ctx, "secret", req); err == nil {
		t.Fatal("expected partial failure")
	}
	if !media.ValidID(sentID) {
		t.Fatalf("sent media ID = %q", sentID)
	}
	refs, err := s.store.MediaReferences().ListMediaIDs(ctx, sentID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("failed event refs %v %v", refs, err)
	}
}

func TestWorkspaceMediaBoundary(t *testing.T) {
	ctx := context.Background()
	s, close := newTestService(t, nil)
	defer close()
	enableMedia(t, s)
	workspace := filepath.Join(s.sandboxRoot, "elnis", "watcher")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "input.png"), []byte("workspace"), 0600); err != nil {
		t.Fatal(err)
	}
	if items, err := s.store.Media().DeleteOrphans(ctx, time.Now().Add(time.Hour)); err != nil || len(items) != 0 {
		t.Fatalf("work file imported %v %v", items, err)
	}
	item, err := s.ImportWorkspaceMedia(ctx, "watcher", "input.png", media.Input{MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != "input.png" || item.MIMEType != "image/png" {
		t.Fatal(item)
	}
	if err := s.ExportWorkspaceMedia(ctx, "watcher", item.ID, "copy.png"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", `..\outside`, "/absolute", `C:\outside`} {
		if err := s.ExportWorkspaceMedia(ctx, "watcher", item.ID, path); err == nil {
			t.Fatalf("export escaped %s", path)
		}
		if _, err := s.ImportWorkspaceMedia(ctx, "watcher", path, media.Input{}); err == nil {
			t.Fatalf("import escaped %s", path)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err == nil {
		if err := s.ExportWorkspaceMedia(ctx, "watcher", item.ID, "link/escape.png"); err == nil {
			t.Fatal("symlink export escaped")
		}
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
