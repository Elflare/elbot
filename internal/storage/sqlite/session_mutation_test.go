package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"elbot/internal/storage"
)

func TestSessionMutateReadsLatestAndRollsBack(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	row := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local", Mode: storage.SessionModeWork, Title: "original", Metadata: `{"unknown":9007199254740993,"count":0}`}
	if err := store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	errs := make(chan error, 24)
	for range 24 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := store.Sessions().Mutate(ctx, row.ID, func(latest *storage.Session) error {
				fields, err := storage.DecodeSessionMetadata(latest.Metadata)
				if err != nil {
					return err
				}
				var n int
				if err := json.Unmarshal(fields["count"], &n); err != nil {
					return err
				}
				if err := fields.Set("count", n+1); err != nil {
					return err
				}
				latest.Metadata, err = fields.Encode()
				return err
			})
			errs <- err
		}()
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := store.Sessions().Mutate(ctx, row.ID, func(latest *storage.Session) error { latest.Title = "renamed"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("reject update")
	_, err = store.Sessions().Mutate(ctx, row.ID, func(latest *storage.Session) error { latest.Title = "bad"; latest.Metadata = "{}"; return failure })
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	latest, err := store.Sessions().Get(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := storage.DecodeSessionMetadata(latest.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Title != "renamed" || string(fields["count"]) != "24" || string(fields["unknown"]) != "9007199254740993" {
		t.Fatalf("lost update: %#v", latest)
	}
}
