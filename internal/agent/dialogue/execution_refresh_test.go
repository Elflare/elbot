package dialogue

import (
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func TestRefreshExecutionUsesHandoffMemoryAndPreservesNewToolState(t *testing.T) {
	view := ExecutionView{}
	e := turn.NewExecution("run")
	ctx := turn.WithExecution(contextinfo.WithActor(t.Context(), contextinfo.Actor{ID: "background"}), e)
	row := storage.Session{ID: "session", Mode: storage.SessionModeBackground, Metadata: `{"background_kind":"cron","tool_state":"old"}`}
	for range 3 {
		if _, err := view.RefreshExecution(ctx, &row); err != nil {
			t.Fatal(err)
		}
	}
	e.Adopt(contextinfo.WithActor(t.Context(), contextinfo.Actor{ID: "foreground"}), storage.Session{
		ID: row.ID, Mode: storage.SessionModeWork, OwnerID: "foreground", Platform: "cli", PlatformScopeID: "local",
		Metadata: `{"foreground_origin":{"kind":"cron"},"tool_state":"old"}`,
	})
	row.Metadata = `{"background_kind":"cron","tool_state":"new"}`
	updated, err := view.RefreshExecution(ctx, &row)
	if err != nil {
		t.Fatal(err)
	}
	actor, _ := contextinfo.ActorFromContext(updated)
	if actor.ID != "foreground" || session.IsBackground(&row) || row.OwnerID != "foreground" {
		t.Fatal(actor, row)
	}
	fields, err := storage.DecodeSessionMetadata(row.Metadata)
	if err != nil || string(fields["tool_state"]) != `"new"` {
		t.Fatal("tool state overwritten", row.Metadata, err)
	}
	for range 3 {
		if _, err := view.RefreshExecution(updated, &row); err != nil {
			t.Fatal(err)
		}
	}
	// A tool state commit can bring back promoted metadata before the next call
	// has updated the caller's session mode and ownership.
	row.Mode, row.OwnerID = storage.SessionModeBackground, "background"
	if _, err := view.RefreshExecution(updated, &row); err != nil {
		t.Fatal(err)
	}
	if row.Mode != storage.SessionModeWork || row.OwnerID != "foreground" {
		t.Fatal("promoted metadata prevented updating call permissions", row)
	}
	oldActor, _ := contextinfo.ActorFromContext(ctx)
	if oldActor.ID != "background" {
		t.Fatal("existing call context changed")
	}
}

func TestRefreshExecutionPreservesMetadataFailure(t *testing.T) {
	e := turn.NewExecution("run")
	e.Adopt(t.Context(), storage.Session{ID: "s", Metadata: `{"foreground_origin":{}}`})
	_, err := (ExecutionView{}).RefreshExecution(turn.WithExecution(t.Context(), e), &storage.Session{ID: "s", Metadata: `{broken`})
	if err == nil {
		t.Fatal("invalid state silently accepted")
	}
}
