package agent

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCompleteForkMessageID(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{}
	store := newTestStore(t)
	a := New(p, f, "m", config.ProviderConfig{}, store)
	ctx := context.Background()

	session, err := a.sessions.Create(ctx, a.scope(context.Background()), session.CreateRequest{Title: "completion"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	user := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "question"}
	assistant := &storage.Message{ID: "abcdef-message", SessionID: session.ID, Role: storage.RoleAssistant, Content: "answer"}
	for _, message := range []*storage.Message{user, assistant} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}

	got := a.Complete("/fork abc")
	if len(got) != 1 || got[0] != "/fork abcdef-message" {
		t.Fatalf("Complete = %#v", got)
	}
	if got := a.Complete("/fork no-match"); len(got) != 0 {
		t.Fatalf("Complete no-match = %#v", got)
	}
}

func TestSessionIdleExpiration(t *testing.T) {
	defaultIdleExpiration := config.SessionIdleExpirationConfig{
		GroupUserTTLMinutes:         10,
		GroupSuperadminTTLMinutes:   10,
		PrivateUserTTLMinutes:       10,
		PrivateSuperadminTTLMinutes: 0,
	}
	tests := []struct {
		name        string
		ctx         platform.MessageContext
		superadmin  bool
		cfg         config.SessionIdleExpirationConfig
		wantExpired bool
	}{
		{name: "group user expires", ctx: platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9"}, cfg: defaultIdleExpiration, wantExpired: true},
		{name: "group superadmin expires", ctx: platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9"}, superadmin: true, cfg: defaultIdleExpiration, wantExpired: true},
		{name: "private user expires", ctx: platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "private:1"}, cfg: defaultIdleExpiration, wantExpired: true},
		{name: "private superadmin keeps", ctx: platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "private:1"}, superadmin: true, cfg: defaultIdleExpiration, wantExpired: false},
		{name: "disabled group user keeps", ctx: platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9"}, cfg: config.SessionIdleExpirationConfig{GroupUserTTLMinutes: 0, GroupSuperadminTTLMinutes: 10, PrivateUserTTLMinutes: 10}, wantExpired: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakePlatform{}
			store := newTestStore(t)
			f := &fakeLLM{replies: []string{"fresh reply"}}
			a := New(p, f, "test-model", config.ProviderConfig{}, store)
			if tt.superadmin {
				a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"qq": {"1"}}))
			}
			a.SetSessionIdleExpiration(tt.cfg)
			ctx := platform.WithMessageContext(context.Background(), tt.ctx)

			oldSession, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Title: "old"})
			if err != nil {
				t.Fatalf("create old session: %v", err)
			}
			oldSession.UpdatedAt = time.Now().Add(-11 * time.Minute)
			if err := store.Sessions().Update(ctx, oldSession); err != nil {
				t.Fatalf("age old session: %v", err)
			}

			if err := a.HandleMessage(ctx, "hello again"); err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			_, oldErr := store.Sessions().Get(ctx, oldSession.ID)
			current, err := a.sessions.Current(ctx, a.scope(ctx))
			if err != nil {
				t.Fatalf("current session: %v", err)
			}
			if tt.wantExpired {
				if !errors.Is(oldErr, storage.ErrNotFound) {
					t.Fatalf("old session err = %v, want not found", oldErr)
				}
				if current.ID == oldSession.ID {
					t.Fatal("current session was not replaced")
				}
			} else {
				if oldErr != nil {
					t.Fatalf("old session err = %v, want nil", oldErr)
				}
				if current.ID != oldSession.ID {
					t.Fatalf("current session = %s, want %s", current.ID, oldSession.ID)
				}
			}
		})
	}
}

func TestMessageContextResumeStartsTargetSession(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"resume reply"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9"})

	bg := &storage.Session{OwnerID: "qq:1", Platform: "qq", PlatformScopeID: "cron:user.cron.test", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Title: "cron"}
	if err := store.Sessions().Create(ctx, bg); err != nil {
		t.Fatalf("create background session: %v", err)
	}
	resumeCtx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9", ResumeSessionID: bg.ID})
	if err := a.HandleMessage(resumeCtx, "continue here"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	current, err := a.sessions.Current(resumeCtx, a.scope(resumeCtx))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if current.ID != bg.ID {
		t.Fatalf("current = %s, want %s", current.ID, bg.ID)
	}
	messages, err := store.Messages().ListBySession(resumeCtx, bg.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Content != "continue here" || messages[1].Content != "resume reply" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestMessageContextForkStartsForkSession(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"fork reply"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9"})

	source, err := a.sessions.Create(ctx, a.scope(ctx), session.CreateRequest{Title: "source"})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	assistant := &storage.Message{SessionID: source.ID, Role: storage.RoleAssistant, Content: "answer"}
	if err := store.Messages().Append(ctx, assistant); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	forkCtx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "qq", PlatformUserID: "1", ScopeID: "group:9", ForkFromMessageID: assistant.ID})
	if err := a.HandleMessage(forkCtx, "continue from here"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	current, err := a.sessions.Current(forkCtx, a.scope(forkCtx))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if current.ID == source.ID || current.ParentSessionID != source.ID || current.ForkFromMessageID != assistant.ID {
		t.Fatalf("fork session = %#v, source = %s assistant = %s", current, source.ID, assistant.ID)
	}
	messages, err := store.Messages().ListBySession(forkCtx, current.ID)
	if err != nil {
		t.Fatalf("list fork messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Content != "continue from here" || messages[1].Content != "fork reply" {
		t.Fatalf("fork messages = %#v", messages)
	}
}

func TestChatSchedulesAsyncNamingAndSessionsShowPreview(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"main reply"}, titleReplies: []string{"generated title"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "hello naming"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	waitRequestCount(t, f, 2)

	sessions, err := store.Sessions().List(ctx, storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d", len(sessions))
	}
	deadline := time.Now().Add(time.Second)
	for sessions[0].Title != "generated title" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		sessions, err = store.Sessions().List(ctx, storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
		if err != nil {
			t.Fatalf("list sessions: %v", err)
		}
	}
	if sessions[0].Title != "generated title" {
		t.Fatalf("title = %q", sessions[0].Title)
	}

	if err := a.HandleMessage(ctx, "/sessions"); err != nil {
		t.Fatalf("sessions command: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "preview: u: hello naming / b: main reply") {
		t.Fatalf("missing preview output: %q", got)
	}
}

func TestStatusRestoresUsageFromSessionMetadata(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: "reply", Usage: &llm.Usage{TotalTokens: 123, CacheHitTokens: 7}}}}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)

	if err := a.HandleMessage(context.Background(), "hello usage"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	sessions, err := store.Sessions().List(context.Background(), storage.ListSessionsRequest{ActorID: "cli:local", Platform: p.Name(), PlatformScopeID: "local"})
	if err != nil || len(sessions) == 0 {
		t.Fatalf("list sessions: %#v err=%v", sessions, err)
	}
	resumed := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, store)
	if err := resumed.HandleMessage(context.Background(), "/resume "+sessions[0].ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	p.out.Reset()
	if err := resumed.HandleMessage(context.Background(), "/status"); err != nil {
		t.Fatalf("status: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "tokens：123（命中：7）") {
		t.Fatalf("usage not restored in status: %q", got)
	}
}

func TestModeCommandContinuesWithMessageInActivatedSession(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantMode   string
		wantTools  bool
		wantNotice string
	}{
		{name: "work", input: "/work solve this", wantMode: storage.SessionModeWork, wantTools: true, wantNotice: "work mode active"},
		{name: "chat", input: "/chat talk with me", wantMode: storage.SessionModeChat, wantTools: false, wantNotice: "chat mode active"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakePlatform{}
			f := &fakeLLM{replies: []string{"model reply"}}
			a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
			a.SetToolProvider(&recordingToolProvider{tools: []llm.ToolSchema{{Function: llm.ToolFunctionSchema{Name: "discover_tool", Parameters: map[string]any{"type": "object"}}}}})

			if err := a.HandleMessage(context.Background(), tt.input); err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			requests := f.chatRequests()
			if len(requests) != 1 {
				t.Fatalf("chat requests = %d", len(requests))
			}
			wantText := strings.TrimPrefix(tt.input, "/"+tt.name+" ")
			if got := llm.LatestUserSegmentTextOnly(requests[0].Messages); got != wantText {
				t.Fatalf("latest user text = %q, want %q", got, wantText)
			}
			if got := len(requests[0].Tools) > 0; got != tt.wantTools {
				t.Fatalf("has tools = %v, want %v", got, tt.wantTools)
			}
			current, err := a.sessions.Current(context.Background(), a.scope(context.Background()))
			if err != nil {
				t.Fatalf("Current: %v", err)
			}
			if current.Mode != tt.wantMode {
				t.Fatalf("mode = %q", current.Mode)
			}
			output := p.out.String()
			if notice, reply := strings.Index(output, tt.wantNotice), strings.Index(output, "model reply"); notice < 0 || reply < 0 || notice > reply {
				t.Fatalf("notice and reply order = %q", output)
			}
		})
	}
}

func TestModeCommandWithoutMessageOnlySwitchesMode(t *testing.T) {
	for _, input := range []string{"/work", "/chat"} {
		t.Run(input, func(t *testing.T) {
			p := &fakePlatform{}
			f := &fakeLLM{replies: []string{"unexpected"}}
			a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
			if err := a.HandleMessage(context.Background(), input); err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}
			if got := len(f.chatRequests()); got != 0 {
				t.Fatalf("chat requests = %d", got)
			}
		})
	}
}

func TestModeCommandContinuationPreservesNonTextSegments(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"described"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform: "cli",
		ScopeID:  "local",
		Sender:   p,
		Segments: []platform.MessageSegment{
			{Type: platform.SegmentText, Text: "/chat describe this"},
			{Type: platform.SegmentImage, URL: "data:image/png;base64,abc", MIMEType: "image/png"},
		},
	})

	if err := a.HandleMessage(ctx, "/chat describe this"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d", len(requests))
	}
	segments := llm.LatestUserSegments(requests[0].Messages)
	if len(segments) != 2 || segments[0].Type != llm.SegmentText || segments[0].Text != "describe this" || segments[1].Type != llm.SegmentImage {
		t.Fatalf("latest user segments = %#v", segments)
	}
}

func TestChatCommandSuggestsNewForWorkHistory(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"reply"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "work history"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	p.out.Reset()
	if err := a.HandleMessage(ctx, "/chat do not send"); err != nil {
		t.Fatalf("chat command: %v", err)
	}
	if !strings.Contains(p.out.String(), "run /new then /chat") {
		t.Fatalf("unexpected chat output: %q", p.out.String())
	}
	current, err := a.sessions.Current(ctx, a.scope(context.Background()))
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if current.Mode != storage.SessionModeWork {
		t.Fatalf("mode = %q", current.Mode)
	}
	if got := len(f.chatRequests()); got != 1 {
		t.Fatalf("chat requests = %d, blocked continuation should not run", got)
	}
}

func TestDefaultModeFromStateAppliesToNewSessions(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"chat reply"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.sessions = session.NewServiceWithConfig(store, session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeChat}, a.titleGen, nil)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "hello default chat"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	current, err := a.sessions.Current(ctx, a.scope(context.Background()))
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if current.Mode != storage.SessionModeChat {
		t.Fatalf("mode = %q", current.Mode)
	}
}

func TestNewSessionsResumeCommands(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{replies: []string{"first", "second"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	ctx := context.Background()

	if err := a.HandleMessage(ctx, "hello"); err != nil {
		t.Fatalf("chat hello: %v", err)
	}
	if err := a.HandleMessage(ctx, "/new"); err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := a.HandleMessage(ctx, "after new"); err != nil {
		t.Fatalf("chat after new: %v", err)
	}
	p.out.Reset()
	if err := a.HandleMessage(ctx, "/sessions"); err != nil {
		t.Fatalf("sessions: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "sessions:") || !strings.Contains(got, "[current]") {
		t.Fatalf("missing current sessions output: %q", got)
	}
	p.out.Reset()
	if err := a.HandleMessage(ctx, "/resume 1"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got = p.out.String()
	if !strings.Contains(got, "resumed session:") || !strings.Contains(got, "recent messages:") {
		t.Fatalf("missing resume history output: %q", got)
	}
	if !strings.Contains(got, "user: hello") || !strings.Contains(got, "assistant: first") {
		t.Fatalf("missing resumed messages: %q", got)
	}
	p.out.Reset()
	if err := a.HandleMessage(ctx, "/resume"); err != nil {
		t.Fatalf("resume list: %v", err)
	}
	if strings.Contains(p.out.String(), "[current]") || !strings.Contains(p.out.String(), "[1]") {
		t.Fatalf("resume list should number non-current sessions: %q", p.out.String())
	}
	if err := a.HandleMessage(ctx, "/status"); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(p.out.String(), "session status:") {
		t.Fatalf("missing status output: %q", p.out.String())
	}
}
