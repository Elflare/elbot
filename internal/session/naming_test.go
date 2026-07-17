package session

import (
	"context"
	"elbot/internal/storage"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestServiceRenameMarksManualTitleAndPreservesMetadata(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}

	session, err := svc.Create(ctx, scope, CreateRequest{Title: "old title"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	session.Metadata = `{"discovered_tools":["web_search"]}`
	if err := store.Sessions().Update(ctx, session); err != nil {
		t.Fatalf("update metadata: %v", err)
	}

	renamed, err := svc.Rename(ctx, scope, session.ID, "new title")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed.Title != "new title" {
		t.Fatalf("title = %q", renamed.Title)
	}

	var metadata struct {
		DiscoveredTools []string `json:"discovered_tools"`
		TitleRenamed    bool     `json:"title_renamed"`
		TitleSource     string   `json:"title_source"`
	}
	if err := json.Unmarshal([]byte(renamed.Metadata), &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if len(metadata.DiscoveredTools) != 1 || metadata.DiscoveredTools[0] != "web_search" {
		t.Fatalf("discovered_tools not preserved: %#v", metadata.DiscoveredTools)
	}
	if !metadata.TitleRenamed || metadata.TitleSource != "manual" {
		t.Fatalf("manual rename metadata missing: %#v", metadata)
	}
}

func TestServiceRenameSkipsAutomaticNaming(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	generator := &fakeTitleGenerator{calls: make(chan []storage.Message, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, generator, nil)
	scope := Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	session, err := svc.Create(ctx, scope, CreateRequest{Title: "old title"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Rename(ctx, scope, session.ID, "manual title"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append message: %v", err)
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	ensureNoGeneratorCall(t, generator.calls)
}

func TestServiceNamingTriggerSteps(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		triggerStep int
		messages    []storage.Message
	}{
		{
			name:        "after first user message",
			triggerStep: 1,
			messages:    []storage.Message{{Role: storage.RoleUser, Content: "hello"}},
		},
		{
			name:        "after first assistant answer",
			triggerStep: 2,
			messages:    []storage.Message{{Role: storage.RoleUser, Content: "hello"}, {Role: storage.RoleAssistant, Content: "hi"}},
		},
		{
			name:        "after second user message",
			triggerStep: 3,
			messages:    []storage.Message{{Role: storage.RoleUser, Content: "hello"}, {Role: storage.RoleAssistant, Content: "hi"}, {Role: storage.RoleUser, Content: "again"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			generator := &fakeTitleGenerator{titles: []string{"generated title"}, calls: make(chan []storage.Message, 1)}
			svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: tc.triggerStep}, generator, nil)
			session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, CreateRequest{Title: "fallback"})
			if err != nil {
				t.Fatalf("create session: %v", err)
			}

			for i, message := range tc.messages {
				message.SessionID = session.ID
				if err := store.Messages().Append(ctx, &message); err != nil {
					t.Fatalf("append message %d: %v", i, err)
				}
				if i < tc.triggerStep-1 {
					svc.MaybeScheduleNaming(ctx, session.ID)
					ensureNoGeneratorCall(t, generator.calls)
				}
			}

			svc.MaybeScheduleNaming(ctx, session.ID)
			messages := waitGeneratorCall(t, generator.calls)
			if len(messages) != tc.triggerStep {
				t.Fatalf("generator message count = %d, want %d", len(messages), tc.triggerStep)
			}
			waitTitle(t, store, session.ID, "generated title")
		})
	}
}

func TestServiceMaybeScheduleNamingSkipsRenamedTitle(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	generator := &fakeTitleGenerator{calls: make(chan []storage.Message, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, generator, nil)
	session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, CreateRequest{Title: "Cron title"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session.Metadata = `{"title_renamed":true,"title_source":"cron"}`
	if err := store.Sessions().Update(ctx, session); err != nil {
		t.Fatalf("update session metadata: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append message: %v", err)
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	ensureNoGeneratorCall(t, generator.calls)
}

func TestServiceNamingFailureNotifiesAndKeepsFallbackTitle(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	generator := &fakeTitleGenerator{errs: []error{fmt.Errorf("boom")}, calls: make(chan []storage.Message, 1)}
	notifier := &fakeNamingNotifier{failures: make(chan NamingFailedEvent, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, generator, notifier)
	session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, CreateRequest{Title: "fallback"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append message: %v", err)
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	_ = waitGeneratorCall(t, generator.calls)
	select {
	case event := <-notifier.failures:
		if event.SessionID != session.ID || event.Reason != "generate title" {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for naming failure event")
	}
	got, err := store.Sessions().Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Title != "fallback" {
		t.Fatalf("title = %q, want fallback", got.Title)
	}
}

func TestServiceNamingFailureCanRetryAndSuccessStopsRepeats(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	generator := &fakeTitleGenerator{
		titles: []string{"", "retry title"},
		errs:   []error{fmt.Errorf("first failure"), nil},
		calls:  make(chan []storage.Message, 3),
	}
	notifier := &fakeNamingNotifier{failures: make(chan NamingFailedEvent, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, generator, notifier)
	session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, CreateRequest{Title: "fallback"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append message: %v", err)
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	_ = waitGeneratorCall(t, generator.calls)
	select {
	case <-notifier.failures:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for failure event")
	}

	if err := store.Messages().Append(ctx, &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "hi"}); err != nil {
		t.Fatalf("append retry message: %v", err)
	}
	svc.MaybeScheduleNaming(ctx, session.ID)
	_ = waitGeneratorCall(t, generator.calls)
	waitTitle(t, store, session.ID, "retry title")

	svc.MaybeScheduleNaming(ctx, session.ID)
	ensureNoGeneratorCall(t, generator.calls)
}

func TestServiceNamingUsesOnlyTriggerStepMessages(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	generator := &fakeTitleGenerator{titles: []string{"generated title"}, calls: make(chan []storage.Message, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, generator, nil)
	session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, CreateRequest{Title: "fallback"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i, message := range []storage.Message{
		{Role: storage.RoleUser, Content: "first user"},
		{Role: storage.RoleAssistant, Content: "first assistant"},
		{Role: storage.RoleUser, Content: "second user"},
	} {
		message.SessionID = session.ID
		if err := store.Messages().Append(ctx, &message); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	messages := waitGeneratorCall(t, generator.calls)
	if len(messages) != 1 || messages[0].Content != "first user" {
		t.Fatalf("generator messages = %#v", messages)
	}
}

func TestServiceNamingPlaceholderFallbacksImmediately(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	generator := &fakeTitleGenerator{
		titles: []string{"New session", "should not be used"},
		calls:  make(chan []storage.Message, 2),
	}
	notifier := &fakeNamingNotifier{failures: make(chan NamingFailedEvent, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 1}, generator, notifier)
	session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, CreateRequest{Title: "New session"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "first user title"}); err != nil {
		t.Fatalf("append user: %v", err)
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	_ = waitGeneratorCall(t, generator.calls)
	select {
	case event := <-notifier.failures:
		if event.Reason != "invalid title" || event.GeneratedTitleNormalized != "New session" || event.GeneratedTitleRaw != "New session" {
			t.Fatalf("failure event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for failure event")
	}
	waitTitle(t, store, session.ID, "first user title")

	svc.MaybeScheduleNaming(ctx, session.ID)
	ensureNoGeneratorCall(t, generator.calls)
}

func TestServiceNamingSkipsBlankMessages(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	generator := &fakeTitleGenerator{titles: []string{"generated title"}, calls: make(chan []storage.Message, 1)}
	svc := NewServiceWithNaming(store, NamingConfig{TriggerStep: 2}, generator, nil)
	session, err := svc.Create(ctx, Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}, CreateRequest{Title: "fallback"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i, message := range []storage.Message{
		{Role: storage.RoleUser, Content: ""},
		{Role: storage.RoleUser, Content: "hello"},
		{Role: storage.RoleAssistant, Content: "  \n\t  "},
		{Role: storage.RoleAssistant, Content: "hi"},
	} {
		message.SessionID = session.ID
		if err := store.Messages().Append(ctx, &message); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}

	svc.MaybeScheduleNaming(ctx, session.ID)
	messages := waitGeneratorCall(t, generator.calls)
	if len(messages) != 2 || messages[0].Content != "hello" || messages[1].Content != "hi" {
		t.Fatalf("generator messages = %#v", messages)
	}
}
