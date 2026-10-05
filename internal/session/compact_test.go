package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/storage"
)

func TestCreateCompactedInheritsSourceAndActivatesOnlyForeground(t *testing.T) {
	for _, mode := range []string{storage.SessionModeWork, storage.SessionModeChat, storage.SessionModeBackground} {
		t.Run(mode, func(t *testing.T) {
			svc, store := newTestService(t)
			ctx := context.Background()
			scope := Scope{ActorID: "qq:one", Platform: "qq", PlatformScopeID: "one"}
			var source *storage.Session
			var err error
			if mode == storage.SessionModeBackground {
				source, err = svc.PrepareBackground(ctx, scope, BackgroundRequest{Kind: "cron", Name: "test"})
			} else {
				source, err = svc.Create(ctx, scope, CreateRequest{Mode: mode})
			}
			if err != nil {
				t.Fatal(err)
			}
			var original *Binding
			if mode != storage.SessionModeBackground {
				_, original, err = svc.CurrentBound(ctx, scope)
				if err != nil {
					t.Fatal(err)
				}
				ctx = WithBinding(ctx, original)
			}
			next, err := svc.CreateCompacted(ctx, scope, source.ID, CompactedRequest{ID: "reserved", Title: "compact title", Metadata: `{"unknown":9007199254740993,"nested":{"keep":true},"compact_generation":2,"compact_seed":["prepared"]}`})
			if err != nil {
				t.Fatal(err)
			}
			if next.ID != "reserved" || next.Title != "compact title" || next.Mode != mode || next.OwnerID != source.OwnerID || next.Platform != source.Platform || next.PlatformScopeID != source.PlatformScopeID {
				t.Fatalf("inheritance=%+v", next)
			}
			for _, want := range []string{`"unknown":9007199254740993`, `"nested":{"keep":true}`, `"compact_generation":2`, `"compact_seed":["prepared"]`, `"title_renamed":true`, `"title_source":"compact"`} {
				if !strings.Contains(next.Metadata, want) {
					t.Fatalf("lost %s: %s", want, next.Metadata)
				}
			}
			persisted, err := store.Sessions().Get(ctx, next.ID)
			if err != nil || persisted.Metadata != next.Metadata {
				t.Fatalf("save=%+v err=%v", persisted, err)
			}
			current, binding, err := svc.CurrentBound(ctx, scope)
			if mode == storage.SessionModeBackground {
				if !errors.Is(err, storage.ErrNotFound) {
					t.Fatalf("background activated current: %+v %v", current, err)
				}
			} else if err != nil || current.ID != next.ID || binding == original || original.Valid() {
				t.Fatalf("activation=%+v %v", current, err)
			}
		})
	}
}

type compactFailCreateRepo struct {
	storage.SessionRepository
	err error
}

func (r compactFailCreateRepo) Create(context.Context, *storage.Session) error { return r.err }

func TestCreateCompactedFailureKeepsSourceAndBinding(t *testing.T) {
	for _, failure := range []string{"save", "cancel", "metadata", "binding", "missing", "foreign"} {
		t.Run(failure, func(t *testing.T) {
			svc, store := newTestService(t)
			ctx := context.Background()
			scope := Scope{ActorID: "qq:one", Platform: "qq", PlatformScopeID: "one"}
			source, err := svc.Create(ctx, scope, CreateRequest{Title: "source"})
			if err != nil {
				t.Fatal(err)
			}
			_, original, err := svc.CurrentBound(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			ctx = WithBinding(ctx, original)
			req := CompactedRequest{ID: "reserved", Title: "compressed", Metadata: `{}`}
			sourceID := source.ID
			switch failure {
			case "save":
				svc.store = storeWithSessionRepository{Store: store, sessions: compactFailCreateRepo{SessionRepository: store.Sessions(), err: errors.New("save failed")}}
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "metadata":
				req.Metadata = "invalid"
			case "binding":
				if err := svc.ResetCurrent(context.Background(), scope); err != nil {
					t.Fatal(err)
				}
				if _, err := svc.Resume(context.Background(), scope, source.ID); err != nil {
					t.Fatal(err)
				}
			case "missing":
				sourceID = "missing"
			case "foreign":
				scope.ActorID = "qq:other"
			}
			if _, err := svc.CreateCompacted(ctx, scope, sourceID, req); err == nil {
				t.Fatal("invalid compact accepted")
			}
			if _, err := store.Sessions().Get(context.Background(), req.ID); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("failed compact saved a row: %v", err)
			}
			current, binding, err := svc.CurrentBound(context.Background(), original.Scope())
			if err != nil || current.ID != source.ID {
				t.Fatalf("source binding lost: %+v %v", current, err)
			}
			if failure != "binding" && (binding != original || !original.Valid()) {
				t.Fatal("failure replaced original binding")
			}
		})
	}
}
