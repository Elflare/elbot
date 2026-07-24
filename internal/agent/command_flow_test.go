package agent

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"errors"
	"strings"
	"testing"
)

func TestPlatformMessageReceivedHookConsumeSkipsCommandAndLLM(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformMessageReceived, Name: "test.received.consume", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Outputs = append(event.Outputs, delivery.Text("consumed"))
		event.Control.Consume = true
		return event, nil
	})}); err != nil {
		t.Fatalf("Register received hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "/help"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "consumed") {
		t.Fatalf("platform output = %q", got)
	}
	if strings.Contains(got, "可用命令") || strings.Contains(got, "final") {
		t.Fatalf("consume should skip command and LLM, output = %q", got)
	}
	if got := len(f.chatRequests()); got != 0 {
		t.Fatalf("chat requests = %d, want 0", got)
	}
}

func TestUnknownCommandDoesNotCallLLM(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))

	if err := a.HandleMessage(context.Background(), "/doesnotexist"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if f.requestCount() != 0 {
		t.Fatalf("LLM was called for unknown command")
	}
	if !strings.Contains(p.out.String(), "unknown command: /doesnotexist") {
		t.Fatalf("unexpected output: %q", p.out.String())
	}
}

func TestConfiguredCommandPrefixAlias(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{models: []string{"alpha"}}
	a := NewWithPrefixes(p, f, map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "default", Model: "alpha"},
		storage.SessionModeChat: {Provider: "default", Model: "alpha"},
	}, config.ProviderConfig{}, newTestStore(t), []string{"/", "-"})

	if err := a.HandleMessage(context.Background(), "-help"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if f.requestCount() != 0 {
		t.Fatalf("LLM was called for alias command")
	}
	if !strings.Contains(p.out.String(), "available commands:") || !strings.Contains(p.out.String(), "-help") {
		t.Fatalf("unexpected help output: %q", p.out.String())
	}
}

func TestRegularUserCanUseOwnDataSlashCommands(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"ok"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}}))
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "cli", PlatformUserID: "regular", ScopeID: "regular"})

	if err := a.HandleMessage(ctx, "/new"); err != nil {
		t.Fatalf("/new: %v", err)
	}
	if !strings.Contains(p.out.String(), "new session ready") {
		t.Fatalf("/new output = %q", p.out.String())
	}
	if _, err := a.sessions.Current(ctx, a.scope(ctx)); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("current after /new = %v, want not found", err)
	}
	sessions, err := a.sessions.List(ctx, a.scope(ctx), "", 20)
	if err != nil {
		t.Fatalf("list sessions after /new: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions after /new = %#v, want none", sessions)
	}
	p.out.Reset()

	if _, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Title: "mine"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := a.HandleMessage(ctx, "/sessions"); err != nil {
		t.Fatalf("/sessions: %v", err)
	}
	out := p.out.String()
	if !strings.Contains(out, "sessions:") {
		t.Fatalf("/sessions output = %q", out)
	}
	if strings.Contains(out, "platform: cli/local") && strings.Contains(out, "regular") == false {
		t.Fatalf("regular user should only see own sessions: %q", out)
	}
}

func TestRegularUserCannotUseSuperadminSlashCommands(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"ok"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}}))
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "cli", PlatformUserID: "regular", ScopeID: "regular"})

	for _, cmd := range []string{"/model", "/requests", "/audit", "/log", "/tools", "/clean"} {
		p.out.Reset()
		if err := a.HandleMessage(ctx, cmd); err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		if !strings.Contains(p.out.String(), "需要超级管理员权限") {
			t.Fatalf("%s should be denied for regular user, got %q", cmd, p.out.String())
		}
	}
}
