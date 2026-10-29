package branch_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/session/branch"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func TestBackgroundCompactedRequiresExecutionOwnership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     contextinfo.ConversationKind
		cli      bool
		mismatch string
	}{
		{name: "background entry"},
		{name: "unknown entry", kind: contextinfo.ConversationUnknown},
		{name: "group entry", kind: contextinfo.ConversationGroup},
		{name: "CLI execution", cli: true},
		{name: "other owner", kind: contextinfo.ConversationPrivate, mismatch: "owner"},
		{name: "other platform", kind: contextinfo.ConversationPrivate, mismatch: "platform"},
		{name: "other task", kind: contextinfo.ConversationPrivate, mismatch: "scope"},
		{name: "CLI other owner", cli: true, mismatch: "owner"},
		{name: "CLI other platform", cli: true, mismatch: "platform"},
		{name: "CLI other task", cli: true, mismatch: "scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			source := f.source(t, storage.SessionModeBackground)
			f.source(t, storage.SessionModeWork)
			current := f.c.CurrentBinding(scope())
			f.setBusy(source.ID)
			target := scope()
			target.ConversationKind, target.IsCLI = tc.kind, tc.cli
			switch tc.mismatch {
			case "owner":
				target.ActorID = "other"
			case "platform":
				target.Platform = "other"
			case "scope":
				target.PlatformScopeID = "other-task"
			}
			row, err := f.s.CreateCompacted(t.Context(), target, source.ID, branch.CompactedRequest{ID: "reserved"})
			if tc.mismatch != "" {
				if err == nil || f.count(t) != 2 {
					t.Fatalf("foreign execution accepted: %+v, %v", row, err)
				}
			} else if err != nil || row.Mode != storage.SessionModeBackground || row.OwnerID != source.OwnerID || row.Platform != source.Platform || row.PlatformScopeID != source.PlatformScopeID || f.count(t) != 3 {
				t.Fatalf("background handoff: %+v, %v", row, err)
			}
			if f.c.CurrentBinding(scope()) != current || !current.Valid() {
				t.Fatal("background compact changed foreground binding")
			}
			latest, err := f.store.Sessions().Get(t.Context(), source.ID)
			if err != nil || latest.Mode != source.Mode || latest.Metadata != source.Metadata {
				t.Fatalf("source changed: %+v, %v", latest, err)
			}
		})
	}
}

func TestCompactedInheritanceAndActivation(t *testing.T) {
	for _, mode := range []string{storage.SessionModeWork, storage.SessionModeChat, storage.SessionModeBackground} {
		for _, native := range []bool{false, true} {
			t.Run(mode+map[bool]string{true: "/native", false: "/display"}[native], func(t *testing.T) {
				f := newFixture(t)
				source := f.source(t, mode)
				original := f.c.CurrentBinding(scope())
				ctx := share.WithBinding(t.Context(), original)
				f.setBusy(source.ID)
				req := branch.CompactedRequest{ID: "reserved", Title: "compacted", Metadata: `{"unknown":9007199254740993,"nested":{"keep":true},"compact_generation":2,"llm_origin":{"protocol":"wrong"}}`}
				if native {
					req.Seed = seed()
				}
				row, err := f.s.CreateCompacted(ctx, scope(), source.ID, req)
				if err != nil {
					t.Fatal(err)
				}
				if row.ID != req.ID || row.Mode != mode || row.OwnerID != source.OwnerID || row.Platform != source.Platform || row.PlatformScopeID != source.PlatformScopeID {
					t.Fatalf("inheritance: %+v", row)
				}
				for _, want := range []string{`"unknown":9007199254740993`, `"nested":{"keep":true}`, `"title_source":"compact"`, `"title_renamed":true`, `"protocol":"response"`} {
					if !strings.Contains(row.Metadata, want) {
						t.Fatalf("lost %s: %s", want, row.Metadata)
					}
				}
				if strings.Contains(row.Metadata, "source_only") {
					t.Fatal("inherited source runtime metadata")
				}
				if mode == storage.SessionModeBackground {
					if f.c.CurrentBinding(scope()) != nil {
						t.Fatal("background compact activated current")
					}
				} else {
					if original.Valid() || f.c.CurrentBinding(scope()).SessionID() != row.ID {
						t.Fatal("foreground compact not activated")
					}
					if sessions, bindings := f.releaseCounts(); sessions != 0 || bindings != 1 {
						t.Fatalf("busy source released: %d %d", sessions, bindings)
					}
					f.setBusy()
					if err := f.c.SessionIdle(t.Context(), source.ID); err != nil {
						t.Fatal(err)
					}
					if sessions, _ := f.releaseCounts(); sessions != 1 {
						t.Fatal("completed source retained")
					}
				}
				if native {
					records, err := f.store.Dialogues().LoadNativeWindow(t.Context(), row, nil)
					if err != nil || records.Seed == nil || records.Seed.SessionID != row.ID {
						t.Fatalf("seed: %+v %v", records, err)
					}
				}
			})
		}
	}
}

func TestCompactedFailuresPreserveCurrent(t *testing.T) {
	for _, failure := range []string{"empty-id", "same-id", "metadata", "binding", "missing", "foreign", "cancel", "save", "checkpoint"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			source := f.source(t, storage.SessionModeWork)
			original := f.c.CurrentBinding(scope())
			ctx := share.WithBinding(t.Context(), original)
			req := branch.CompactedRequest{ID: "reserved", Metadata: `{}`}
			sourceID, target := source.ID, scope()
			switch failure {
			case "empty-id":
				req.ID = ""
			case "same-id":
				req.ID = source.ID
			case "metadata":
				req.Metadata = "invalid"
			case "binding":
				if err := f.c.ResetCurrent(t.Context(), scope()); err != nil {
					t.Fatal(err)
				}
				if _, err := f.c.Resume(t.Context(), scope(), source.ID); err != nil {
					t.Fatal(err)
				}
			case "missing":
				sourceID = "missing"
			case "foreign":
				target.ActorID = "other"
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "save":
				f.failSave()
			case "checkpoint":
				req.Seed, req.ExpectedCheckpointID = seed(), "stale"
			}
			before := f.c.CurrentBinding(scope())
			if _, err := f.s.CreateCompacted(ctx, target, sourceID, req); err == nil {
				t.Fatal("invalid compact accepted")
			}
			if f.count(t) != 1 || f.c.CurrentBinding(scope()) != before || !before.Valid() {
				t.Fatal("failure changed source or binding")
			}
		})
	}
}

type promotionGate struct {
	share.BindingCoordinator
	promote func()
}

func (g promotionGate) EnterSessions(ctx context.Context, ids ...string) (context.Context, func(), error) {
	g.promote()
	return g.BindingCoordinator.EnterSessions(ctx, ids...)
}

func TestCompactedRejectsPromotionBeforeAdmission(t *testing.T) {
	f := newFixture(t)
	source := f.source(t, storage.SessionModeBackground)
	f.deps.Bindings = promotionGate{BindingCoordinator: f.c, promote: func() {
		if _, err := f.c.Resume(t.Context(), scope(), source.ID); err != nil {
			t.Fatal(err)
		}
	}}
	f.s = branch.NewService(f.deps)
	if _, err := f.s.CreateCompacted(t.Context(), scope(), source.ID, branch.CompactedRequest{ID: "reserved"}); !errors.Is(err, share.ErrForegroundSession) {
		t.Fatalf("promotion: %v", err)
	}
	if f.count(t) != 1 || f.c.CurrentBinding(scope()).SessionID() != source.ID {
		t.Fatal("promotion overwritten")
	}
}

func TestCompactedStartsNewBindingTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		source := f.source(t, storage.SessionModeWork)
		if err := f.c.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Second)
		if _, err := f.s.CreateCompacted(context.Background(), scope(), source.ID, branch.CompactedRequest{ID: "reserved"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if f.c.CurrentBinding(scope()) == nil {
			t.Fatal("compact inherited old deadline")
		}
		time.Sleep(31 * time.Second)
		synctest.Wait()
		if f.c.CurrentBinding(scope()) != nil {
			t.Fatal("compact bypassed timer")
		}
	})
}
