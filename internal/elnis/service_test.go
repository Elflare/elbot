package elnis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/storage/sqlite"
	"elbot/internal/toolrun"
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

func TestHandleAllowsConfiguredElwispWithoutEnabled(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	req := testRequest(ModeRecord)
	req.Elwisp.Name = "configured"
	resp, err := service.Handle(context.Background(), "secret", req)
	if err != nil {
		t.Fatalf("Handle configured elwisp without enabled: %v", err)
	}
	if !resp.Accepted || resp.Status != StatusCompleted {
		t.Fatalf("response = %#v", resp)
	}
}

func TestHandleAllowsExplicitEnabledElwisp(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	req := testRequest(ModeRecord)
	req.Elwisp.Name = "enabled"
	resp, err := service.Handle(context.Background(), "secret", req)
	if err != nil {
		t.Fatalf("Handle explicitly enabled elwisp: %v", err)
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

func TestHandleRejectsTokenNotAllowedForElwisp(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	req := testRequest(ModeRecord)
	resp, err := service.Handle(context.Background(), "alt-secret", req)
	if err == nil {
		t.Fatal("expected token restriction error")
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

func TestHandleAllowsConfiguredInternalTool(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	req := testRequest(ModeLLM)
	req.ToolListNames = []string{"shell"}
	resp, err := service.Handle(context.Background(), "secret", req)
	if err != nil {
		t.Fatalf("Handle allowed tool: %v", err)
	}
	if resp.Status != StatusQueued {
		t.Fatalf("response = %#v", resp)
	}
}

func TestHandleRejectsDisallowedInternalTool(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	req := testRequest(ModeLLM)
	req.ToolListNames = []string{"cron"}
	resp, err := service.Handle(context.Background(), "secret", req)
	if err == nil {
		t.Fatal("expected disallowed internal tool error")
	}
	if resp.Accepted || !strings.Contains(resp.Error, "not allowed") {
		t.Fatalf("response = %#v", resp)
	}
}

func TestHandleAllowsExternalToolsByDefault(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	req := testRequest(ModeLLM)
	req.Tools = []toolrun.ELwispToolDeclaration{testExternalTool("server_status")}
	resp, err := service.Handle(context.Background(), "secret", req)
	if err != nil {
		t.Fatalf("Handle external tool: %v", err)
	}
	if resp.Status != StatusQueued {
		t.Fatalf("response = %#v", resp)
	}
	record, err := service.store.ElnisEvents().GetByKey(context.Background(), req.Elwisp.Name, req.Source, req.ID)
	if err != nil {
		t.Fatalf("GetByKey: %v", err)
	}
	if record.ToolDeclarations == "" || record.ToolHash == "" {
		t.Fatalf("tool declaration not persisted: %#v", record)
	}
}

func TestHandleRejectsDisabledExternalTool(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	req := testRequest(ModeLLM)
	req.Tools = []toolrun.ELwispToolDeclaration{testExternalTool("danger_tool")}
	resp, err := service.Handle(context.Background(), "secret", req)
	if err == nil {
		t.Fatal("expected disabled external tool error")
	}
	if resp.Accepted || !strings.Contains(resp.Error, "disabled") {
		t.Fatalf("response = %#v", resp)
	}
}

func TestRunLLMEventUsesModelSlot(t *testing.T) {
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":false,"report":""}`}
	service, cleanup := newTestServiceWithRunner(t, runner, nil)
	defer cleanup()
	var queued QueuedLLMEvent
	service.SetLLMEnqueuer(func(ctx context.Context, event QueuedLLMEvent) error {
		queued = event
		return nil
	})

	req := testRequest(ModeLLM)
	req.ModelSlot = "elwisp2"
	if _, err := service.Handle(context.Background(), "secret", req); err != nil {
		t.Fatalf("Handle llm: %v", err)
	}
	if err := service.RunLLMEvent(context.Background(), queued.Event, queued.EventID); err != nil {
		t.Fatalf("RunLLMEvent: %v", err)
	}
	if len(runner.requests) != 1 || runner.requests[0].ModelProvider != "provider-elwisp2" || runner.requests[0].Model != "model-elwisp2" {
		t.Fatalf("requests = %#v", runner.requests)
	}
}

func TestRunLLMEventFallsBackToWorkModel(t *testing.T) {
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":false,"report":""}`}
	service, cleanup := newTestServiceWithRunner(t, runner, nil)
	defer cleanup()
	var queued QueuedLLMEvent
	service.SetLLMEnqueuer(func(ctx context.Context, event QueuedLLMEvent) error {
		queued = event
		return nil
	})

	req := testRequest(ModeLLM)
	if _, err := service.Handle(context.Background(), "secret", req); err != nil {
		t.Fatalf("Handle llm: %v", err)
	}
	if err := service.RunLLMEvent(context.Background(), queued.Event, queued.EventID); err != nil {
		t.Fatalf("RunLLMEvent: %v", err)
	}
	if len(runner.requests) != 1 || runner.requests[0].ModelProvider != "provider-work" || runner.requests[0].Model != "model-work" {
		t.Fatalf("requests = %#v", runner.requests)
	}
}

func TestRunLLMEventFallsBackWhenSlotUnconfigured(t *testing.T) {
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":false,"report":""}`}
	service, cleanup := newTestServiceWithRunner(t, runner, nil)
	defer cleanup()
	service.resolveModel = func(slot string) config.ModelSelection {
		if slot == "work" {
			return config.ModelSelection{Provider: "provider-work", Model: "model-work"}
		}
		return config.ModelSelection{}
	}
	var queued QueuedLLMEvent
	service.SetLLMEnqueuer(func(ctx context.Context, event QueuedLLMEvent) error {
		queued = event
		return nil
	})

	req := testRequest(ModeLLM)
	req.ModelSlot = "elwisp3"
	if _, err := service.Handle(context.Background(), "secret", req); err != nil {
		t.Fatalf("Handle llm: %v", err)
	}
	if err := service.RunLLMEvent(context.Background(), queued.Event, queued.EventID); err != nil {
		t.Fatalf("RunLLMEvent: %v", err)
	}
	if len(runner.requests) != 1 || runner.requests[0].ModelProvider != "provider-work" || runner.requests[0].Model != "model-work" {
		t.Fatalf("requests = %#v", runner.requests)
	}
}

func TestHandleRejectsInvalidModelSlot(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	req := testRequest(ModeLLM)
	req.ModelSlot = "chat"
	resp, err := service.Handle(context.Background(), "secret", req)
	if err == nil {
		t.Fatal("expected invalid model_slot error")
	}
	if resp.Accepted || !strings.Contains(resp.Error, "unsupported model_slot") {
		t.Fatalf("response = %#v", resp)
	}
}

func TestHandleRejectsElwispNameWithDot(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()

	req := testRequest(ModeLLM)
	req.Elwisp.Name = "foo.bar"
	resp, err := service.Handle(context.Background(), "secret", req)
	if err == nil {
		t.Fatal("expected invalid elwisp.name error")
	}
	if resp.Accepted || !strings.Contains(resp.Error, "elwisp.name") {
		t.Fatalf("response = %#v", resp)
	}
}

func TestRunLLMEventCompletesAndReports(t *testing.T) {
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"处理完成"}`}
	sent := []string{}
	service, cleanup := newTestServiceWithRunner(t, runner, func(ctx context.Context, target delivery.Target, out delivery.Output) error {
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

func TestRunLLMEventReportsSegmentsByRelativePath(t *testing.T) {
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"见图","report_segments":[{"type":"image","url":"chart.png"}]}`}
	sent := []delivery.Kind{}
	service, cleanup := newTestServiceWithRunner(t, runner, func(ctx context.Context, target delivery.Target, out delivery.Output) error {
		sent = append(sent, out.Kind)
		return nil
	})
	defer cleanup()
	if err := os.MkdirAll(filepath.Join(service.sandboxRoot, "elnis", "watcher"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.sandboxRoot, "elnis", "watcher", "chart.png"), []byte("png"), 0644); err != nil {
		t.Fatal(err)
	}
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
	if len(sent) != 2 || sent[0] != delivery.KindText || sent[1] != delivery.KindImage {
		t.Fatalf("sent kinds = %#v", sent)
	}
}

func TestRunLLMEventRejectsAbsoluteReportSegmentPath(t *testing.T) {
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"见图","report_segments":[{"type":"image","url":"/tmp/chart.png"}]}`}
	service, cleanup := newTestServiceWithRunner(t, runner, func(ctx context.Context, target delivery.Target, out delivery.Output) error { return nil })
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
	if err := service.RunLLMEvent(context.Background(), queued.Event, queued.EventID); err == nil {
		t.Fatal("expected absolute report segment path error")
	}
}

func TestRunLLMEventReportsFailedResultWhenRequested(t *testing.T) {
	runner := &fakeBackgroundRunner{text: `{"completed":false,"need_report":true,"report":"创建失败：工具权限不足"}`}
	sent := []string{}
	service, cleanup := newTestServiceWithRunner(t, runner, func(ctx context.Context, target delivery.Target, out delivery.Output) error {
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
	service, cleanup := newTestService(t, func(ctx context.Context, target delivery.Target, out delivery.Output) error {
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
		send = func(ctx context.Context, target delivery.Target, out delivery.Output) error { return nil }
	}
	service, err := NewService(Options{
		Config: config.ElnisConfig{
			Enabled:      true,
			AllowedTools: []string{"web_search"},
			Delivery: config.ElnisDeliveryConfig{
				DefaultPlatforms: []string{"cli"},
				AllowSuperadmins: true,
			},
			Elwisps: map[string]config.ElnisElwispConfig{
				"watcher":    {AllowedTokens: []string{"home"}, AllowedTools: []string{"shell"}, DisabledExternalTools: []string{"danger_tool"}},
				"configured": {},
				"enabled":    {Enabled: boolPtr(true)},
				"disabled":   {Enabled: boolPtr(false)},
			},
		},
		SandboxRoot: filepath.Join(t.TempDir(), "sandbox"),
		Tokens:      map[string]string{"home": "secret", "alt": "alt-secret"},
		Store:       store,
		Send:        send,
		Runner:      runner,
		ResolveModel: func(slot string) config.ModelSelection {
			return config.ModelSelection{Provider: "provider-" + slot, Model: "model-" + slot}
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service, func() { _ = store.Close() }
}

func boolPtr(value bool) *bool {
	return &value
}

func testExternalTool(name string) toolrun.ELwispToolDeclaration {
	return toolrun.ELwispToolDeclaration{
		Name:           name,
		Description:    "test external tool",
		Endpoint:       "http://127.0.0.1:32171/tools/" + name,
		TimeoutSeconds: 5,
		Schema:         map[string]any{"type": "object", "properties": map[string]any{"detail": map[string]any{"type": "boolean"}}},
	}
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
