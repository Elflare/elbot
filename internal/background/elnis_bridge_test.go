package background

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/background/elnis"
	"elbot/internal/background/share"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/media"
	"elbot/internal/storage"
	"elbot/internal/testutil"
	"elbot/internal/testutil/mediatest"
	"elbot/internal/tool"
)

func newElnisBridgeFixture(t *testing.T) (elnisBridge, storage.Store) {
	t.Helper()
	store := testutil.NewSQLiteStore(t)
	root := t.TempDir()
	return elnisBridge{repo: store.ElnisEvents(), media: mediatest.New(t, media.MediaCenterDeps{Store: store}, root)}, store
}
func elnisBridgeRequest(mode string) elnis.Request {
	return elnis.Request{Version: "elvena.v2", Elwisp: elnis.Elwisp{Name: "watcher"}, Source: "source", ID: "event", Mode: mode, Format: "text", Content: "hello", Targets: []elnis.Target{{Platform: "cli"}}}
}
func newElnisBridgeService(t *testing.T, b elnisBridge, opts elnis.Options) *elnis.Service {
	t.Helper()
	opts.Store = b
	opts.Media = b
	opts.Tokens = map[string]string{"home": "secret"}
	opts.Config.Enabled = true
	opts.Config.Elwisps = map[string]config.ElnisElwispConfig{"watcher": {AllowedTokens: []string{"home"}}}
	opts.EnabledPlatforms = []string{"cli"}
	opts.SessionTakenOver = func(context.Context, string) (bool, error) { return false, nil }
	opts.ToolPreloader = emptyElnisSelections{}
	opts.ResolveModel = func(string) config.ModelSelection { return config.ModelSelection{Provider: "provider", Model: "model"} }
	if opts.Send == nil {
		opts.Send = func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
			return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
		}
	}
	svc, err := elnis.NewService(opts)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetLLMEnqueuer(func(context.Context, elnis.QueuedLLMEvent) error { return nil })
	return svc
}

type emptyElnisSelections struct{}

func (emptyElnisSelections) BackgroundSelections(context.Context, []string) ([]tool.BackgroundSelection, error) {
	return nil, nil
}
func seedElnisReportMessage(t *testing.T, store storage.Store) {
	t.Helper()
	ctx := t.Context()
	if err := store.Sessions().Create(ctx, &storage.Session{ID: "session", OwnerID: "owner", Platform: "cli", PlatformScopeID: "elnis:test", Mode: storage.SessionModeBackground, Status: storage.SessionStatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{ID: "message", SessionID: "session", Role: storage.RoleAssistant, Content: "report"}); err != nil {
		t.Fatal(err)
	}
}
func TestElnisBridgeRecordsAndTerminalReferences(t *testing.T) {
	b, store := newElnisBridgeFixture(t)
	ctx := t.Context()
	info, err := b.ImportReader(ctx, strings.NewReader("input"), 5, media.Input{Name: "input.txt"}, media.ImportLimits{})
	id := info.ID
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	req := elnis.CreateEventRequest{MediaIDs: []string{id}, EventKey: "key", TokenName: "token", ElwispName: "watcher", Source: "source", SourceID: "one", Tags: `["tag"]`, Mode: elnis.ModeLLM, ModelSlot: "slot", ContentHash: "hash", ToolDeclarations: "{opaque tools", ToolHash: "toolhash", RequestedTargets: "{opaque requested", ResolvedTargets: "{opaque resolved", Status: elnis.StatusQueued, ReceivedAt: now, CreatedAt: now.Add(-time.Second)}
	row, err := b.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.ElnisEvents().Get(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.TokenName != req.TokenName || saved.ElwispName != req.ElwispName || saved.Source != req.Source || saved.SourceID != req.SourceID || saved.Tags != req.Tags || saved.Mode != req.Mode || saved.ModelSlot != req.ModelSlot || saved.ToolDeclarations != req.ToolDeclarations || saved.ToolHash != req.ToolHash || saved.RequestedTargets != req.RequestedTargets || saved.ResolvedTargets != req.ResolvedTargets || saved.Status != req.Status || !saved.ReceivedAt.Equal(req.ReceivedAt) || !saved.CreatedAt.Equal(req.CreatedAt) {
		t.Fatalf("lost create fields: %#v", saved)
	}
	got, err := b.GetByKey(ctx, "watcher", "source", "one")
	if err != nil || *got != *row {
		t.Fatalf("record=%#v %v", got, err)
	}
	if _, err := b.Create(ctx, req); err == nil {
		t.Fatal("duplicate accepted")
	}
	for _, read := range []func() error{func() error { _, err := b.Get(ctx, "missing"); return err }, func() error { _, err := b.GetByKey(ctx, "missing", "", ""); return err }} {
		if err := read(); !errors.Is(err, elnis.ErrEventNotFound) {
			t.Fatal(err)
		}
	}
	if err := b.Update(ctx, elnis.UpdateEventRequest{ID: "missing"}); !errors.Is(err, elnis.ErrEventNotFound) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.Get(canceled, row.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	refs, err := store.MediaReferences().ListMediaIDs(ctx, id)
	if err != nil || len(refs) != 1 {
		t.Fatal(refs, err)
	}
	if err := b.Update(ctx, elnis.UpdateEventRequest{ID: row.ID, Status: elnis.StatusFailed, SessionID: "session", Result: "result", Error: "failure", ResolvedTargets: "[]"}); err != nil {
		t.Fatal(err)
	}
	got, err = b.Get(ctx, row.ID)
	if err != nil || got.SessionID != "session" || got.Result != "result" || got.ResolvedTargets != "[]" {
		t.Fatal(got, err)
	}
	saved, err = store.ElnisEvents().Get(ctx, row.ID)
	if err != nil || saved.Status != elnis.StatusFailed || saved.Error != "failure" {
		t.Fatal(saved, err)
	}
	refs, err = store.MediaReferences().ListMediaIDs(ctx, id)
	if err != nil || len(refs) != 0 {
		t.Fatal(refs, err)
	}
}

type duplicateElnisReceiptRepo struct{ storage.ElnisEventRepository }

func (r duplicateElnisReceiptRepo) MarkReportDeliveriesDelivered(ctx context.Context, ids []string, receipt string, mappings []storage.PlatformMessageMap) error {
	mappings[0].ID = "duplicate-map"
	mappings = append(mappings, mappings[0])
	return r.ElnisEventRepository.MarkReportDeliveriesDelivered(ctx, ids, receipt, mappings)
}
func TestElnisBridgeReportTransactionsAndRecovery(t *testing.T) {
	b, store := newElnisBridgeFixture(t)
	ctx := t.Context()
	seedElnisReportMessage(t, store)
	row, err := b.Create(ctx, elnis.CreateEventRequest{EventKey: "key", ElwispName: "watcher", Source: "source", SourceID: "one", Mode: elnis.ModeLLM, Status: elnis.StatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	info, err := b.ImportReader(ctx, strings.NewReader("report"), 6, media.Input{Name: "report.txt"}, media.ImportLimits{})
	id := info.ID
	if err != nil {
		t.Fatal(err)
	}
	if err := b.AddEventReference(ctx, id, row.ID, "report"); err != nil {
		t.Fatal(err)
	}
	if err := b.PrepareReport(ctx, elnis.PrepareReportRequest{EventID: row.ID, SessionID: "session", ResolvedTargets: "[]", Result: "report", ResultReadyStatus: elnis.StatusResultReady, Deliveries: []elnis.CreateReportDeliveryRequest{{Target: `{"platform":"target-1"}`, Output: `{"text":"output-1"}`, MessageID: "message"}, {Target: `{"platform":"target-2"}`, Output: `{"text":"output-2"}`, MessageID: "message"}}}); err != nil {
		t.Fatal(err)
	}
	ids, err := b.ListResultReadyReportIDs(ctx, elnis.StatusResultReady)
	if err != nil || len(ids) != 1 || ids[0] != row.ID {
		t.Fatal(ids, err)
	}
	if ok, err := b.ClaimReport(ctx, row.ID, elnis.StatusResultReady, elnis.StatusDelivering); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := b.ClaimReport(ctx, row.ID, elnis.StatusResultReady, elnis.StatusDelivering); err != nil || ok {
		t.Fatal(ok, err)
	}
	items, err := b.ListReportDeliveries(ctx, row.ID)
	if err != nil || len(items) != 2 || items[0].Target != `{"platform":"target-1"}` || items[1].Output != `{"text":"output-2"}` || items[0].MessageID != "message" {
		t.Fatal(items, err)
	}
	if err := b.StartReportDelivery(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	mappings := []share.ReportMessageMap{{Platform: "telegram", PlatformScopeID: "private:1", PlatformMessageID: "77", SessionID: "session", MessageID: "message"}}
	broken := b
	broken.repo = duplicateElnisReceiptRepo{b.repo}
	if err := broken.MarkReportDeliveriesDelivered(ctx, []string{items[0].ID}, "receipt", mappings); err == nil {
		t.Fatal("expected transaction failure")
	}
	if _, err := store.Messages().FindByPlatformMessage(ctx, "telegram", "private:1", "77"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("mapping survived rollback: %v", err)
	}
	pending, err := store.ElnisEvents().ListReportDeliveries(ctx, row.ID)
	if err != nil || pending[0].Status != storage.ElnisReportDeliveryPending || pending[0].Receipt != "" {
		t.Fatal(pending, err)
	}
	if err := b.MarkReportDeliveryFailed(ctx, row.ID, items[0].ID, elnis.StatusResultReady, "partial failure", "partial receipt", mappings); err != nil {
		t.Fatal(err)
	}
	mapped, err := store.Messages().FindByPlatformMessage(ctx, "telegram", "private:1", "77")
	if err != nil || mapped.ID != "message" {
		t.Fatal(mapped, err)
	}
	saved, err := store.ElnisEvents().Get(ctx, row.ID)
	if err != nil || saved.Status != elnis.StatusResultReady || saved.Error != "partial failure" {
		t.Fatal(saved, err)
	}
	if ok, err := b.ClaimReport(ctx, row.ID, elnis.StatusResultReady, elnis.StatusDelivering); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := b.ResetDeliveringReports(ctx, elnis.StatusDelivering, elnis.StatusResultReady); err != nil {
		t.Fatal(err)
	}
	if ok, err := b.ClaimReport(ctx, row.ID, elnis.StatusResultReady, elnis.StatusDelivering); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := b.ReleaseReport(ctx, row.ID, elnis.StatusDelivering, elnis.StatusResultReady, "retry"); err != nil {
		t.Fatal(err)
	}
	if ok, err := b.ClaimReport(ctx, row.ID, elnis.StatusResultReady, elnis.StatusDelivering); err != nil || !ok {
		t.Fatal(ok, err)
	}
	for _, item := range items {
		if err := b.StartReportDelivery(ctx, item.ID); err != nil {
			t.Fatal(err)
		}
	}
	mappings[0].PlatformMessageID = "78"
	mappings = append(mappings, share.ReportMessageMap{Platform: "telegram", PlatformScopeID: "private:2", PlatformMessageID: "78", SessionID: "session", MessageID: "message"})
	if err := b.MarkReportDeliveriesDelivered(ctx, []string{items[0].ID, items[1].ID}, "complete receipt", mappings); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"private:1", "private:2"} {
		msg, err := store.Messages().FindByPlatformMessage(ctx, "telegram", scope, "78")
		if err != nil || msg.ID != "message" {
			t.Fatal(msg, err)
		}
	}
	if err := b.CompleteReport(ctx, row.ID, elnis.StatusDelivering, elnis.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	refs, err := store.MediaReferences().ListMediaIDs(ctx, id)
	if err != nil || len(refs) != 0 {
		t.Fatal(refs, err)
	}
	items, err = b.ListReportDeliveries(ctx, row.ID)
	if err != nil || items[0].Status != elnis.ReportDeliveryDelivered || items[1].Status != elnis.ReportDeliveryDelivered {
		t.Fatal(items, err)
	}
}

func TestElnisBridgeImportLimitsArePerCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, _ := newElnisBridgeFixture(t)
		server := testutil.NewVirtualDefaultHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(50 * time.Millisecond):
				_, _ = io.WriteString(w, "1234")
			case <-r.Context().Done():
			}
		}))
		if _, err := b.ImportURL(t.Context(), server.URL, media.Input{Name: "file.txt"}, media.ImportLimits{MaxImportBytes: 3, DownloadTimeout: time.Second}); err == nil {
			t.Fatal("size limit ignored")
		}
		if _, err := b.ImportURL(t.Context(), server.URL, media.Input{Name: "file.txt"}, media.ImportLimits{MaxImportBytes: 4, DownloadTimeout: time.Second}); err != nil {
			t.Fatal(err)
		}
		if _, err := b.ImportURL(t.Context(), server.URL, media.Input{Name: "file.txt"}, media.ImportLimits{MaxImportBytes: 4, DownloadTimeout: time.Millisecond}); err == nil {
			t.Fatal("timeout ignored")
		}
		if _, err := b.ImportURL(t.Context(), server.URL, media.Input{}, media.ImportLimits{}); err != nil {
			t.Fatal("per-call limits changed default imports:", err)
		}
	})
}

type elnisBridgeRunner struct{ result string }

func (r elnisBridgeRunner) RunBackground(context.Context, share.RunRequest) (share.RunResult, error) {
	return share.RunResult{SessionID: "session", MessageID: "message", Text: r.result}, nil
}
func TestElnisBridgeReportMediaSurvivesWorkspaceRemoval(t *testing.T) {
	b, store := newElnisBridgeFixture(t)
	seedElnisReportMessage(t, store)
	ctx := t.Context()
	root := t.TempDir()
	dir := filepath.Join(root, "elnis", "watcher")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chart.png"), []byte("report image"), 0600); err != nil {
		t.Fatal(err)
	}
	sendErr := errors.New("offline")
	svc := newElnisBridgeService(t, b, elnis.Options{SandboxRoot: root, Runner: elnisBridgeRunner{result: `{"completed":true,"need_report":true,"report":"result","report_segments":[{"type":"image","url":"chart.png"}]}`}, Send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultFailed}, sendErr
	}})
	var queued elnis.QueuedLLMEvent
	svc.SetLLMEnqueuer(func(_ context.Context, e elnis.QueuedLLMEvent) error { queued = e; return nil })
	if _, err := svc.Handle(ctx, "secret", elnisBridgeRequest(elnis.ModeLLM)); err != nil {
		t.Fatal(err)
	}
	if err := svc.RunLLMEvent(ctx, queued.Event, queued.EventID); !errors.Is(err, sendErr) {
		t.Fatal(err)
	}
	items, err := b.ListReportDeliveries(ctx, queued.EventID)
	if err != nil || len(items) != 2 {
		t.Fatal(items, err)
	}
	var out delivery.Output
	if err := json.Unmarshal([]byte(items[1].Output), &out); err != nil {
		t.Fatal(err)
	}
	if !media.ValidID(out.Source.MediaID) || out.Source.Path != "" || out.Source.URL != "" {
		t.Fatal(out)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	data, _, err := b.media.Read(ctx, out.Source.MediaID)
	if err != nil || string(data) != "report image" {
		t.Fatalf("lost report: %q %v", data, err)
	}
	refs, err := store.MediaReferences().ListMediaIDs(ctx, out.Source.MediaID)
	if err != nil || len(refs) != 1 {
		t.Fatal(refs, err)
	}
	if err := b.Update(ctx, elnis.UpdateEventRequest{ID: queued.EventID, Status: elnis.StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	refs, err = store.MediaReferences().ListMediaIDs(ctx, out.Source.MediaID)
	if err != nil || len(refs) != 0 {
		t.Fatal(refs, err)
	}
	if issues, err := store.Media().CheckReferences(ctx); err != nil || len(issues) != 0 {
		t.Fatal(issues, err)
	}
}

func TestElnisBridgeCancelledEnqueueReleasesInput(t *testing.T) {
	b, store := newElnisBridgeFixture(t)
	info, err := b.ImportReader(t.Context(), strings.NewReader("input"), 5, media.Input{Name: "input.txt"}, media.ImportLimits{})
	id := info.ID
	if err != nil {
		t.Fatal(err)
	}
	svc := newElnisBridgeService(t, b, elnis.Options{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	svc.SetLLMEnqueuer(func(ctx context.Context, _ elnis.QueuedLLMEvent) error { cancel(); return ctx.Err() })
	req := elnisBridgeRequest(elnis.ModeLLM)
	req.Segments = []elnis.Segment{{Kind: elnis.SegmentKindFile, URL: id}}
	if _, err := svc.Handle(ctx, "secret", req); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	row, err := store.ElnisEvents().GetByKey(t.Context(), "watcher", "source", "event")
	if err != nil || row.Status != elnis.StatusFailed {
		t.Fatal(row, err)
	}
	refs, err := store.MediaReferences().ListMediaIDs(t.Context(), id)
	if err != nil || len(refs) != 0 {
		t.Fatal(refs, err)
	}
}

func TestElnisBridgeURLImportAndReferenceValidation(t *testing.T) {
	b, store := newElnisBridgeFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "url media") }))
	defer server.Close()
	info, err := b.ImportURL(t.Context(), server.URL, media.Input{Name: "download.txt", MIMEType: "text/plain"}, media.ImportLimits{})
	id := info.ID
	if err != nil || !media.ValidID(id) {
		t.Fatal(id, err)
	}
	data, item, err := b.media.Read(t.Context(), id)
	if err != nil || string(data) != "url media" || item.Name != "download.txt" {
		t.Fatal(item, err)
	}
	if err := b.AddEventReference(t.Context(), id, "event", "report"); err != nil {
		t.Fatal(err)
	}
	refs, err := store.MediaReferences().ListMediaIDs(t.Context(), id)
	if err != nil || len(refs) != 1 || refs[0].OwnerType != "elnis_event" || refs[0].OwnerID != "event" || refs[0].Purpose != "report" {
		t.Fatal(refs, err)
	}
	if err := b.AddEventReference(t.Context(), "media:"+strings.Repeat("f", 64), "event", "report"); err == nil {
		t.Fatal("accepted missing media")
	}
	if _, err := b.ImportURL(t.Context(), "invalid", media.Input{}, media.ImportLimits{}); err == nil {
		t.Fatal("discarded import error")
	}
}

func TestElnisBridgeDirectCancellationReleasesImportedMedia(t *testing.T) {
	b, store := newElnisBridgeFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var id string
	svc := newElnisBridgeService(t, b, elnis.Options{Send: func(ctx context.Context, _ delivery.Target, out []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		id = out[0].Source.MediaID
		cancel()
		return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultCanceled}, ctx.Err()
	}})
	req := elnisBridgeRequest(elnis.ModeDirect)
	req.Segments = []elnis.Segment{{Kind: elnis.SegmentKindFile, URL: "data:text/plain;base64,aGVsbG8=", Name: "input.txt"}}
	if _, err := svc.Handle(ctx, "secret", req); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !media.ValidID(id) {
		t.Fatal(id)
	}
	refs, err := store.MediaReferences().ListMediaIDs(t.Context(), id)
	if err != nil || len(refs) != 0 {
		t.Fatal(refs, err)
	}
}

func TestElnisBridgeServiceDeduplicatesAndPersistsReportMappings(t *testing.T) {
	b, store := newElnisBridgeFixture(t)
	seedElnisReportMessage(t, store)
	sends := 0
	svc := newElnisBridgeService(t, b, elnis.Options{SandboxRoot: t.TempDir(), Runner: elnisBridgeRunner{result: `{"completed":true,"need_report":true,"report":"done"}`}, Send: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		sends++
		return delivery.Receipt{SentMessages: []delivery.SentMessage{{Platform: "telegram", ScopeID: "private:1", PlatformMessageID: "77"}, {Platform: "telegram", ScopeID: "private:2", PlatformMessageID: "77"}}}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}})
	var queued elnis.QueuedLLMEvent
	enqueues := 0
	svc.SetLLMEnqueuer(func(_ context.Context, e elnis.QueuedLLMEvent) error { queued = e; enqueues++; return nil })
	req := elnisBridgeRequest(elnis.ModeLLM)
	if _, err := svc.Handle(t.Context(), "secret", req); err != nil {
		t.Fatal(err)
	}
	duplicate, err := svc.Handle(t.Context(), "secret", req)
	if err != nil || !duplicate.Duplicate || enqueues != 1 {
		t.Fatal(duplicate, err, enqueues)
	}
	if err := svc.RunLLMEvent(t.Context(), queued.Event, queued.EventID); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"private:1", "private:2"} {
		row, err := store.Messages().FindByPlatformMessage(t.Context(), "telegram", scope, "77")
		if err != nil || row.ID != "message" {
			t.Fatal(row, err)
		}
	}
	row, err := store.ElnisEvents().Get(t.Context(), queued.EventID)
	if err != nil || row.Status != elnis.StatusCompleted || sends != 1 {
		t.Fatal(row, err, sends)
	}
}

func TestRecoverInterruptedPreservesDurableReportsAndTerminalEvents(t *testing.T) {
	ctx := t.Context()
	b, store := newElnisBridgeFixture(t)
	repo := store.ElnisEvents()
	const mediaID = "report-media"
	if err := store.Media().Upsert(ctx, &storage.Media{ID: mediaID, Backend: "local", MIMEType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	before := map[string]*storage.ElnisEvent{}
	for _, status := range []string{elnis.StatusAccepted, elnis.StatusQueued, elnis.StatusRunning, elnis.StatusCompleted, elnis.StatusFailed, elnis.StatusTakenOver, elnis.StatusResultReady, elnis.StatusDelivering} {
		var mediaIDs []string
		if status == elnis.StatusAccepted || status == elnis.StatusQueued || status == elnis.StatusRunning {
			mediaIDs = []string{mediaID}
		}
		row, err := repo.Create(ctx, storage.CreateElnisEventRequest{
			EventKey: status, ElwispName: "recovery", Source: "test", SourceID: status,
			Status: status, Error: "existing error", Result: "existing result", MediaIDs: mediaIDs,
		})
		if err != nil {
			t.Fatal(err)
		}
		if status == elnis.StatusResultReady || status == elnis.StatusDelivering {
			if err := repo.PrepareReport(ctx, storage.PrepareElnisReportRequest{
				EventID: row.ID, ResultReadyStatus: status, Result: "durable report",
				Deliveries: []storage.CreateElnisReportDeliveryRequest{{Target: "{}", Output: `{"Source":{"media":"report-media"}}`}},
			}); err != nil {
				t.Fatal(err)
			}
		}
		before[status], err = repo.Get(ctx, row.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	for pass := range 2 {
		if err := elnis.RecoverInterrupted(ctx, b); err != nil {
			t.Fatal(err)
		}
		for status, previous := range before {
			row, err := repo.Get(ctx, previous.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus, wantError := previous.Status, previous.Error
			interrupted := status == elnis.StatusAccepted || status == elnis.StatusQueued || status == elnis.StatusRunning
			if interrupted {
				wantStatus, wantError = elnis.StatusFailed, "interrupted before durable report"
			}
			if row.Status != wantStatus || row.Error != wantError || row.Result != previous.Result {
				t.Fatalf("%s: recovered event = %#v", status, row)
			}
			if (pass > 0 || !interrupted) && !row.UpdatedAt.Equal(previous.UpdatedAt) {
				t.Fatalf("%s: unchanged event timestamp was rewritten", status)
			}
			before[status] = row
			if status == elnis.StatusResultReady || status == elnis.StatusDelivering {
				items, err := repo.ListReportDeliveries(ctx, row.ID)
				if err != nil || len(items) != 1 || items[0].Output != `{"Source":{"media":"report-media"}}` {
					t.Fatalf("%s: outbox changed: %#v, %v", status, items, err)
				}
			}
		}
		refs, err := store.MediaReferences().ListMediaIDs(ctx, mediaID)
		if err != nil || len(refs) != 2 {
			t.Fatalf("durable report references = %#v, %v", refs, err)
		}
	}
}

func TestQueuedMediaReferencesRecoverAfterInterruption(t *testing.T) {
	ctx := context.Background()
	b, store := newElnisBridgeFixture(t)
	s := newElnisBridgeService(t, b, elnis.Options{})
	item, err := b.media.ImportBytes(ctx, []byte("queued"), media.Input{Name: "input.png"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	req := elnisBridgeRequest(elnis.ModeLLM)
	req.Segments = []elnis.Segment{{Kind: elnis.SegmentKindImage, URL: item.ID}}
	if _, err := s.Handle(ctx, "secret", req); err != nil {
		t.Fatal(err)
	}
	refs, err := store.MediaReferences().ListMediaIDs(ctx, item.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("queue refs %v %v", refs, err)
	}
	if err := store.Media().RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	refs, err = store.MediaReferences().ListMediaIDs(ctx, item.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("media cleanup changed event references: %v, %v", refs, err)
	}
	row, err := store.ElnisEvents().GetByKey(ctx, req.Elwisp.Name, req.Source, req.ID)
	if err != nil || row.Status != elnis.StatusQueued {
		t.Fatalf("media cleanup changed event: %#v, %v", row, err)
	}
	if err := elnis.RecoverInterrupted(ctx, b); err != nil {
		t.Fatal(err)
	}
	refs, err = store.MediaReferences().ListMediaIDs(ctx, item.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("interrupted refs %v %v", refs, err)
	}
}

type blockedElnisRunner struct{ entered, release chan struct{} }

func (r blockedElnisRunner) RunBackground(ctx context.Context, _ share.RunRequest) (share.RunResult, error) {
	close(r.entered)
	<-r.release
	return share.RunResult{}, ctx.Err()
}

func TestRuntimeCloseCommitsInterruptedEventsAndPreservesOutbox(t *testing.T) {
	b, store := newElnisBridgeFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	svc := newElnisBridgeService(t, b, elnis.Options{SandboxRoot: t.TempDir(), Runner: blockedElnisRunner{entered, release}})
	cfg := config.Default().Elnis.HTTP
	cfg.Addr = "127.0.0.1:0"
	cfg.Workers = 1
	runtime := elnis.NewRuntime(cfg, svc)
	if err := runtime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	req := elnisBridgeRequest(elnis.ModeLLM)
	if _, err := svc.Handle(t.Context(), "secret", req); err != nil {
		t.Fatal(err)
	}
	<-entered
	runtime.BeginClose()
	// All workers are canceled; the occupied execution keeps cleanup from starting.
	const mediaID = "shutdown-media"
	if err := store.Media().Upsert(t.Context(), &storage.Media{ID: mediaID, Backend: "local", MIMEType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{}
	for _, status := range []string{elnis.StatusAccepted, elnis.StatusQueued, elnis.StatusRunning, elnis.StatusResultReady, elnis.StatusDelivering} {
		var mediaIDs []string
		if status == elnis.StatusAccepted || status == elnis.StatusQueued || status == elnis.StatusRunning {
			mediaIDs = []string{mediaID}
		}
		row, err := store.ElnisEvents().Create(t.Context(), storage.CreateElnisEventRequest{
			EventKey: status, ElwispName: "close", Source: "test", SourceID: status, Status: status, MediaIDs: mediaIDs,
		})
		if err != nil {
			t.Fatal(err)
		}
		rows[status] = row.ID
		if status == elnis.StatusResultReady || status == elnis.StatusDelivering {
			if err := store.ElnisEvents().PrepareReport(t.Context(), storage.PrepareElnisReportRequest{
				EventID: row.ID, ResultReadyStatus: status, Result: "durable",
				Deliveries: []storage.CreateElnisReportDeliveryRequest{{Target: "{}", Output: `{"Source":{"media":"shutdown-media"}}`}},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	select {
	case <-runtime.Done():
		t.Fatal("closed before writer exit")
	default:
	}
	close(release)
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	for status, id := range rows {
		row, err := store.ElnisEvents().Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if status == elnis.StatusResultReady || status == elnis.StatusDelivering {
			items, err := store.ElnisEvents().ListReportDeliveries(t.Context(), id)
			if err != nil || len(items) != 1 || row.Status != status || row.Result != "durable" {
				t.Fatalf("outbox lost: %#v %#v %v", row, items, err)
			}
		} else if row.Status != elnis.StatusFailed || row.Error != "elnis stopped before durable report" {
			t.Fatalf("unfinished event not finalized: %#v", row)
		}
	}
	refs, err := store.MediaReferences().ListMediaIDs(t.Context(), mediaID)
	if err != nil || len(refs) != 2 {
		t.Fatalf("remaining references=%#v %v", refs, err)
	}
}
