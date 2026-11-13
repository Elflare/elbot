package session

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"testing"

	"elbot/internal/storage"
)

func TestPrepareBackgroundKeepsCurrentAndPreservesOtherFields(t *testing.T) {
	s, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u"}, Platform: "cli", ChatID: "local", ChatType: "private"}
	created, err := s.Create(ctx, scope, CreateRequest{})
	if err != nil || created.Status != operationresult.Success {
		t.Fatalf("create: %+v %v", created, err)
	}
	current := created.Data
	currentResult, err := s.CurrentBound(ctx, scope)
	if err != nil || currentResult.Status != operationresult.Success {
		t.Fatalf("current: %+v %v", currentResult, err)
	}
	binding := currentResult.Data.Binding
	prepared, err := s.PrepareBackground(ctx, scope, BackgroundRequest{TaskType: "cron", TaskID: "task"})
	if err != nil || prepared.Status != operationresult.Success {
		t.Fatalf("prepare background: %+v %v", prepared, err)
	}
	row := prepared.Data
	if row.Mode != storage.SessionModeBackground || row.Title != "Cron: task" || !IsBackground(row) {
		t.Fatalf("background=%+v", row)
	}
	if _, err := store.Sessions().Mutate(ctx, row.ID, func(row *storage.Session) error {
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		if err := fields.Set("unknown", 9007199254740993); err != nil {
			return err
		}
		row.Metadata, err = fields.Encode()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reusedResult, err := s.PrepareBackground(ctx, scope, BackgroundRequest{SessionID: row.ID, TaskType: "cron", TaskID: "task", Metadata: map[string]string{"cron_job_name": "task"}})
	if err != nil || reusedResult.Status != operationresult.Success {
		t.Fatalf("reuse background: %+v %v", reusedResult, err)
	}
	reused := reusedResult.Data
	fields, err := storage.DecodeSessionMetadata(reused.Metadata)
	if err != nil || string(fields["unknown"]) != "9007199254740993" || reused.Mode != storage.SessionModeBackground {
		t.Fatalf("reused=%+v err=%v", reused, err)
	}
	latestResult, err := s.CurrentBound(ctx, scope)
	if err != nil || latestResult.Status != operationresult.Success || latestResult.Data.Session.ID != current.ID || binding != latestResult.Data.Binding || !binding.Valid() {
		t.Fatalf("background changed foreground current: %+v %v", latestResult, err)
	}
	promotedResult, err := s.Resume(ctx, scope, row.ID)
	if err != nil || promotedResult.Status != operationresult.Success {
		t.Fatalf("promote: %+v %v", promotedResult, err)
	}
	promoted, err := store.Sessions().Get(ctx, row.ID)
	if err != nil || promoted.Mode != storage.SessionModeWork || IsBackground(promoted) {
		t.Fatalf("promotion did not switch mode: %+v err=%v", promoted, err)
	}
	if result, err := s.PrepareBackground(ctx, scope, BackgroundRequest{SessionID: row.ID, TaskType: "cron", TaskID: "task"}); err != nil || result.Status != operationresult.Conflict {
		t.Fatalf("promoted session reused by background: %+v %v", result, err)
	}
}
