package session

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/storage"
)

func TestPrepareBackgroundKeepsCurrentAndPreservesOtherFields(t *testing.T) {
	s, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "u", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	current, err := s.Create(ctx, scope, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, binding, err := s.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.PrepareBackground(ctx, scope, BackgroundRequest{Kind: "cron", Name: "task"})
	if err != nil {
		t.Fatal(err)
	}
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
	reused, err := s.PrepareBackground(ctx, scope, BackgroundRequest{SessionID: row.ID, Kind: "cron", Name: "task", Metadata: map[string]string{"cron_job_name": "task"}})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := storage.DecodeSessionMetadata(reused.Metadata)
	if err != nil || string(fields["unknown"]) != "9007199254740993" || reused.Mode != storage.SessionModeBackground {
		t.Fatalf("reused=%+v err=%v", reused, err)
	}
	latest, latestBinding, err := s.CurrentBound(ctx, scope)
	if err != nil || latest.ID != current.ID || binding != latestBinding || !binding.Valid() {
		t.Fatalf("background changed foreground current: %+v %v", latest, err)
	}
	if _, err := s.Resume(ctx, scope, row.ID); err != nil {
		t.Fatal(err)
	}
	promoted, err := store.Sessions().Get(ctx, row.ID)
	if err != nil || promoted.Mode != storage.SessionModeWork || IsBackground(promoted) {
		t.Fatalf("promotion did not switch mode: %+v err=%v", promoted, err)
	}
	if _, err := s.PrepareBackground(ctx, scope, BackgroundRequest{SessionID: row.ID, Kind: "cron", Name: "task"}); !errors.Is(err, ErrForegroundSession) {
		t.Fatalf("promoted session reused by background: %v", err)
	}
}
