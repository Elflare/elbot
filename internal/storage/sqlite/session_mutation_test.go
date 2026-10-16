package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

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

func TestSessionMutatePreservesAndChangesStoredValues(t *testing.T) {
	for _, name := range []string{"same values", "activity only", "in-place archive", "in-place pin", "clear timestamps"} {
		t.Run(name, func(t *testing.T) {
			store := newTestStore(t)
			at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			archived, pinned := at, at
			row := &storage.Session{Title: "kept", Metadata: `{"unknown":9007199254740993}`, CreatedAt: at, UpdatedAt: at, ArchivedAt: &archived, PinnedAt: &pinned}
			if err := store.Sessions().Create(t.Context(), row); err != nil {
				t.Fatal(err)
			}
			updated, err := store.Sessions().Mutate(t.Context(), row.ID, func(latest *storage.Session) error {
				switch name {
				case "same values":
					value := latest.ArchivedAt.In(time.FixedZone("same instant", 3600))
					latest.ArchivedAt = &value
				case "activity only":
					latest.UpdatedAt = at.Add(time.Hour)
				case "in-place archive":
					*latest.ArchivedAt = at.Add(time.Hour)
				case "in-place pin":
					*latest.PinnedAt = at.Add(time.Hour)
				case "clear timestamps":
					latest.ArchivedAt, latest.PinnedAt = nil, nil
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := store.Sessions().Get(t.Context(), row.ID)
			if err != nil || stored.Title != "kept" || stored.Metadata != row.Metadata || !stored.CreatedAt.Equal(at) {
				t.Fatalf("stored=%+v returned=%+v error=%v", stored, updated, err)
			}
			activity := at
			if name == "activity only" {
				activity = at.Add(time.Hour)
			}
			if !stored.UpdatedAt.Equal(activity) || !updated.UpdatedAt.Equal(activity) {
				t.Fatalf("activity stored=%v returned=%v want=%v", stored.UpdatedAt, updated.UpdatedAt, activity)
			}
			if name == "clear timestamps" {
				if stored.ArchivedAt != nil || stored.PinnedAt != nil {
					t.Fatalf("timestamps not cleared: %+v", stored)
				}
				return
			}
			wantArchive, wantPin := at, at
			if name == "in-place archive" {
				wantArchive = at.Add(time.Hour)
			}
			if name == "in-place pin" {
				wantPin = at.Add(time.Hour)
			}
			if stored.ArchivedAt == nil || !stored.ArchivedAt.Equal(wantArchive) || stored.PinnedAt == nil || !stored.PinnedAt.Equal(wantPin) {
				t.Fatalf("unexpected timestamps: %+v", stored)
			}
		})
	}
}

func TestSessionMutateCancellationDoesNotReportSuccess(t *testing.T) {
	store := newTestStore(t)
	row := &storage.Session{Title: "kept"}
	if err := store.Sessions().Create(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, err := store.Sessions().Mutate(ctx, row.ID, func(*storage.Session) error { cancel(); return nil }); err == nil {
		t.Fatal("canceled mutation reported success")
	}
}
