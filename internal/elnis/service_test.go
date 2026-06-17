package elnis

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/output"
	"elbot/internal/storage/sqlite"
)

func TestHandleRecordCreatesEventAndRejectsDuplicate(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	req := testRequest(ModeRecord)
	resp, err := service.Handle(context.Background(), "secret", req)
	if err != nil {
		t.Fatalf("Handle record: %v", err)
	}
	if !resp.Accepted || resp.Duplicate || resp.Status != StatusCompleted {
		t.Fatalf("response = %#v", resp)
	}

	resp, err = service.Handle(context.Background(), "secret", req)
	if err != nil {
		t.Fatalf("Handle duplicate: %v", err)
	}
	if !resp.Accepted || !resp.Duplicate || resp.Status != StatusDuplicate {
		t.Fatalf("duplicate response = %#v", resp)
	}
}

func TestHandleRejectsUnauthorizedToken(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	resp, err := service.Handle(context.Background(), "wrong", testRequest(ModeRecord))
	if err == nil {
		t.Fatal("expected unauthorized error")
	}
	if resp.Accepted || resp.Status != StatusFailed {
		t.Fatalf("response = %#v", resp)
	}
}

func TestHandleAllowsUnconfiguredElwisp(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	req := testRequest(ModeRecord)
	req.Elwisp.Name = "unknown"
	resp, err := service.Handle(context.Background(), "secret", req)
	if err != nil {
		t.Fatalf("Handle unconfigured elwisp: %v", err)
	}
	if !resp.Accepted || resp.Status != StatusCompleted {
		t.Fatalf("response = %#v", resp)
	}
}

func TestHandleRejectsDisabledElwisp(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	req := testRequest(ModeRecord)
	req.Elwisp.Name = "disabled"
	resp, err := service.Handle(context.Background(), "secret", req)
	if err == nil {
		t.Fatal("expected disabled elwisp error")
	}
	if resp.Accepted || resp.Status != StatusFailed {
		t.Fatalf("response = %#v", resp)
	}
}

func TestHandleLLMQueuesEvent(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()
	var queued QueuedLLMEvent
	service.SetLLMEnqueuer(func(ctx context.Context, event QueuedLLMEvent) error {
		queued = event
		return nil
	})

	req := testRequest(ModeLLM)
	resp, err := service.Handle(context.Background(), "secret", req)
	if err != nil {
		t.Fatalf("Handle llm: %v", err)
	}
	if resp.Status != StatusQueued || queued.EventID == "" || queued.Event.EventKey == "" {
		t.Fatalf("response = %#v queued = %#v", resp, queued)
	}
	record, err := service.store.ElnisEvents().GetByKey(context.Background(), req.Elwisp.Name, req.Source, req.ID)
	if err != nil {
		t.Fatalf("GetByKey: %v", err)
	}
	if record.Status != StatusQueued {
		t.Fatalf("status = %q", record.Status)
	}
}

func TestRunLLMEventCompletesAndReports(t *testing.T) {
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"处理完成"}`}
	sent := []string{}
	service, cleanup := newTestServiceWithRunner(t, runner, func(ctx context.Context, target output.Target, out output.Output) error {
		sent = append(sent, target.Platform+":"+out.Text)
		return nil
	})
	defer cleanup()
	var queued QueuedLLMEvent
	service.SetLLMEnqueuer(func(ctx context.Context, event QueuedLLMEvent) error {
		queued = event
		return nil
	})

	req := testRequest(ModeLLM)
	req.Targets = Targets{Platforms: []string{"cli"}, Superadmins: true}
	if _, err := service.Handle(context.Background(), "secret", req); err != nil {
		t.Fatalf("Handle llm: %v", err)
	}
	if err := service.RunLLMEvent(context.Background(), queued.Event, queued.EventID); err != nil {
		t.Fatalf("RunLLMEvent: %v", err)
	}
	record, err := service.store.ElnisEvents().GetByKey(context.Background(), req.Elwisp.Name, req.Source, req.ID)
	if err != nil {
		t.Fatalf("GetByKey: %v", err)
	}
	if record.Status != StatusCompleted || record.SessionID != "bg-session" || !strings.Contains(record.Result, "处理完成") {
		t.Fatalf("record = %#v", record)
	}
	if len(sent) != 1 || sent[0] != "cli:处理完成" {
		t.Fatalf("sent = %#v", sent)
	}
	if len(runner.requests) != 1 || runner.requests[0].Kind != background.KindElnis || runner.requests[0].SandboxSubdir != "elnis/watcher" {
		t.Fatalf("requests = %#v", runner.requests)
	}
}

func TestRunLLMEventReportsFailedResultWhenRequested(t *testing.T) {
	runner := &fakeBackgroundRunner{text: `{"completed":false,"need_report":true,"report":"创建失败：工具权限不足"}`}
	sent := []string{}
	service, cleanup := newTestServiceWithRunner(t, runner, func(ctx context.Context, target output.Target, out output.Output) error {
		sent = append(sent, target.Platform+":"+out.Text)
		return nil
	})
	defer cleanup()
	var queued QueuedLLMEvent
	service.SetLLMEnqueuer(func(ctx context.Context, event QueuedLLMEvent) error {
		queued = event
		return nil
	})

	req := testRequest(ModeLLM)
	req.Targets = Targets{Platforms: []string{"cli"}, Superadmins: true}
	if _, err := service.Handle(context.Background(), "secret", req); err != nil {
		t.Fatalf("Handle llm: %v", err)
	}
	if err := service.RunLLMEvent(context.Background(), queued.Event, queued.EventID); err != nil {
		t.Fatalf("RunLLMEvent: %v", err)
	}
	if len(sent) != 1 || sent[0] != "cli:创建失败：工具权限不足" {
		t.Fatalf("sent = %#v", sent)
	}
}

func TestResolveTargetsAllUsesAllowedPlatforms(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()
	service.cfg.Delivery.DefaultPlatforms = []string{"cli", "qqonebot"}

	req := testRequest(ModeDirect)
	req.Targets = Targets{Platforms: []string{"all", "missing"}, Superadmins: true}
	resolved, err := service.resolveTargets(req)
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if got := strings.Join(resolved.Platforms, ","); got != "cli,qqonebot" {
		t.Fatalf("platforms = %q", got)
	}
}

func TestHandleDirectSendsToResolvedSuperadmins(t *testing.T) {
	sent := []string{}
	service, cleanup := newTestService(t, func(ctx context.Context, target output.Target, out output.Output) error {
		sent = append(sent, target.Platform+":"+out.Text)
		return nil
	})
	defer cleanup()

	req := testRequest(ModeDirect)
	req.Title = "警告"
	req.Content = "服务器异常"
	req.Targets = Targets{Platforms: []string{"cli", "qqofficial"}}
	resp, err := service.Handle(context.Background(), "secret", req)
	if err != nil {
		t.Fatalf("Handle direct: %v", err)
	}
	if resp.Status != StatusCompleted {
		t.Fatalf("response = %#v", resp)
	}
	if len(sent) != 1 || sent[0] != "cli:警告\n服务器异常" {
		t.Fatalf("sent = %#v", sent)
	}
}

func newTestService(t *testing.T, send SenderFunc) (*Service, func()) {
	return newTestServiceWithRunner(t, nil, send)
}

func newTestServiceWithRunner(t *testing.T, runner background.Runner, send SenderFunc) (*Service, func()) {
	t.Helper()
	store, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	if send == nil {
		send = func(ctx context.Context, target output.Target, out output.Output) error { return nil }
	}
	service, err := NewService(Options{
		Config: config.ElnisConfig{
			Enabled: true,
			Delivery: config.ElnisDeliveryConfig{
				DefaultPlatforms: []string{"cli"},
				AllowSuperadmins: true,
			},
			Elwisps: map[string]config.ElnisElwispConfig{
				"watcher":  {Enabled: true, AllowedTokens: []string{"home"}},
				"disabled": {Enabled: false},
			},
		},
		Tokens: map[string]string{"home": "secret"},
		Store:  store,
		Send:   send,
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service, func() { _ = store.Close() }
}

type fakeBackgroundRunner struct {
	text     string
	requests []background.RunRequest
}

func (r *fakeBackgroundRunner) RunBackground(ctx context.Context, req background.RunRequest) (background.RunResult, error) {
	r.requests = append(r.requests, req)
	return background.RunResult{SessionID: "bg-session", Text: r.text}, nil
}

func testRequest(mode string) Request {
	return Request{
		Version: "elvena.v1",
		Elwisp:  Elwisp{Name: "watcher", Tags: []string{"test"}},
		Source:  "source",
		ID:      "event-1",
		Mode:    mode,
		Format:  "text",
		Content: "hello",
	}
}
