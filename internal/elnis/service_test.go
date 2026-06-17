package elnis

import (
	"context"
	"path/filepath"
	"testing"

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
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service, func() { _ = store.Close() }
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
