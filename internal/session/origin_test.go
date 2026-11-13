package session

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
)

func TestOriginRegistrationPreservesHistoryAndOtherMetadata(t *testing.T) {
	svc, store := newTestService(t)
	ctx := t.Context()
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}, Platform: "cli", ChatID: "local", ChatType: "private"}
	created, err := svc.Create(ctx, scope, CreateRequest{Metadata: `{"unknown":9007199254740993,"workspace_dir":"/work"}`})
	if err != nil || created.Status != operationresult.Success {
		t.Fatalf("create: %+v %v", created, err)
	}
	row := created.Data
	if _, known, err := Origin(row); err != nil || known {
		t.Fatalf("new session origin = %v, %v", known, err)
	}
	target := llm.Origin{APIType: llm.APITypeChat, Provider: "first", BaseURL: "https://first.invalid/v1"}
	row, err = svc.RegisterOrigin(ctx, row.ID, target)
	if err != nil {
		t.Fatal(err)
	}
	changed := llm.Origin{APIType: llm.APITypeChat, Provider: "second", BaseURL: "https://second.invalid/v1"}
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
	if _, err := svc.RegisterOrigin(ctx, row.ID, llm.Origin{APIType: llm.APITypeResponse, Provider: "other"}); err == nil {
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
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "local"}, Platform: "cli", ChatID: "local", ChatType: "private"}
	created, err := svc.Create(ctx, scope, CreateRequest{Metadata: `{"llm_origin":{"protocol":"chat"},"unknown":true}`})
	if err != nil || created.Status != operationresult.Success {
		t.Fatalf("create: %+v %v", created, err)
	}
	row := created.Data
	current, err := svc.CurrentBound(ctx, scope)
	if err != nil || current.Status != operationresult.Success {
		t.Fatalf("current: %+v %v", current, err)
	}
	original := current.Data.Binding
	old := WithBinding(ctx, original)
	reset, err := svc.ResetCurrent(ctx, scope)
	if err != nil || reset.Status != operationresult.Success {
		t.Fatalf("reset: %+v %v", reset, err)
	}
	resumed, err := svc.Resume(ctx, scope, row.ID)
	if err != nil || resumed.Status != operationresult.Success {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	old = contextinfo.WithExecution(old, contextinfo.Execution{SessionID: row.ID, RunID: "fabricated", Attempt: "fabricated"})
	target := llm.Origin{APIType: llm.APITypeChat, Provider: "actual", BaseURL: "https://actual.invalid"}
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
	current, err = svc.CurrentBound(ctx, scope)
	if err != nil || current.Status != operationresult.Success {
		t.Fatalf("current: %+v %v", current, err)
	}
	fresh := current.Data.Binding
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
