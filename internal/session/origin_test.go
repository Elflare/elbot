package session

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/storage"
)

func TestOriginRegistrationPreservesHistoryAndOtherMetadata(t *testing.T) {
	svc, store := newTestService(t)
	ctx := t.Context()
	scope := Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	row, err := svc.Create(ctx, scope, CreateRequest{Metadata: `{"unknown":9007199254740993,"workspace_dir":"/work"}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, known, err := Origin(row); err != nil || known {
		t.Fatalf("new session origin = %v, %v", known, err)
	}
	target := llm.Origin{Protocol: llm.ProtocolChat, Provider: "first", BaseURL: "https://first.invalid/v1"}
	row, err = svc.RegisterOrigin(ctx, row.ID, target)
	if err != nil {
		t.Fatal(err)
	}
	changed := llm.Origin{Protocol: llm.ProtocolChat, Provider: "second", BaseURL: "https://second.invalid/v1"}
	row, err = svc.RegisterOrigin(ctx, row.ID, changed)
	if err != nil {
		t.Fatal(err)
	}
	if origin, known, err := Origin(row); err != nil || !known || origin != target {
		t.Fatalf("historical origin overwritten: %+v, %v, %v", origin, known, err)
	}
	for _, want := range []string{`"unknown":9007199254740993`, `"workspace_dir":"/work"`} {
		if !strings.Contains(row.Metadata, want) {
			t.Fatalf("lost %s: %s", want, row.Metadata)
		}
	}
	before := row.Metadata
	if _, err := svc.RegisterOrigin(ctx, row.ID, llm.Origin{Protocol: llm.ProtocolResponse, Provider: "other"}); err == nil {
		t.Fatal("existing material was silently reinterpreted")
	}
	persisted, err := store.Sessions().Get(ctx, row.ID)
	if err != nil || persisted.Metadata != before {
		t.Fatalf("failed registration changed metadata: %+v, %v", persisted, err)
	}
}

func TestOriginRegistrationRejectsStaleBindingAndKeepsPartialChat(t *testing.T) {
	svc, store := newTestService(t)
	ctx := t.Context()
	scope := Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	row, err := svc.Create(ctx, scope, CreateRequest{Metadata: `{"llm_origin":{"protocol":"chat"},"unknown":true}`})
	if err != nil {
		t.Fatal(err)
	}
	_, original, err := svc.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	old := WithBinding(ctx, original)
	if err := svc.ResetCurrent(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resume(ctx, scope, row.ID); err != nil {
		t.Fatal(err)
	}
	old = contextinfo.WithExecution(old, contextinfo.Execution{SessionID: row.ID, RunID: "fabricated", Attempt: "fabricated"})
	target := llm.Origin{Protocol: llm.ProtocolChat, Provider: "actual", BaseURL: "https://actual.invalid"}
	if _, err := svc.RegisterOrigin(old, row.ID, target); err == nil {
		t.Fatal("public associations authorized an expired binding")
	}
	persisted, err := store.Sessions().Get(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if origin, _, err := Origin(persisted); err != nil || origin.Provider != "" {
		t.Fatalf("rejected registration fabricated a historical provider: %+v, %v", origin, err)
	}
	_, fresh, err := svc.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	row, err = svc.RegisterOrigin(WithBinding(ctx, fresh), row.ID, target)
	if err != nil {
		t.Fatal(err)
	}
	if origin, _, err := Origin(row); err != nil || origin != target {
		t.Fatalf("partial Chat origin was not completed: %+v, %v", origin, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.RegisterOrigin(canceled, row.ID, target); err == nil {
		t.Fatal("canceled registration was admitted")
	}
}

func TestOriginInheritanceDoesNotImportRuntimeMetadata(t *testing.T) {
	source := &storage.Session{Metadata: `{"llm_origin":{"protocol":"chat","provider":"historical","base_url":"https://old.invalid"},"active_attempt":"old"}`}
	inherited, err := InheritOrigin(source, `{"unknown":9007199254740993,"llm_origin":{"protocol":"response","provider":"wrong"}}`)
	if err != nil {
		t.Fatal(err)
	}
	copyOrigin, _, err := Origin(&storage.Session{Metadata: inherited})
	if err != nil || copyOrigin.Provider != "historical" || copyOrigin.Protocol != llm.ProtocolChat || strings.Contains(inherited, "active_attempt") || !strings.Contains(inherited, "9007199254740993") {
		t.Fatalf("inheritance = %s, %v", inherited, err)
	}
	inherited, err = InheritOrigin(&storage.Session{}, inherited)
	if err != nil {
		t.Fatal(err)
	}
	if _, known, err := Origin(&storage.Session{Metadata: inherited}); err != nil || known {
		t.Fatalf("missing source borrowed target ownership: %s, %v", inherited, err)
	}
}
