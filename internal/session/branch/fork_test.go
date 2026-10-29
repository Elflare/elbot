package branch_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/session/branch"
	"elbot/internal/session/control"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func TestForkModesAndOwnership(t *testing.T) {
	for _, mode := range []string{storage.SessionModeWork, storage.SessionModeChat, storage.SessionModeBackground} {
		for _, entry := range []string{"owner", "other-user", "other-platform", "other-scope", "cli-owner", "cli-other", "group"} {
			t.Run(mode+"/"+entry, func(t *testing.T) {
				f := newFixture(t)
				source := f.source(t, mode)
				message := f.message(t, source.ID, storage.RoleAssistant)
				target := scope()
				allowed := entry == "owner" || entry == "cli-owner"
				switch entry {
				case "other-user":
					target.ActorID = "other"
				case "other-platform":
					target.Platform = "other"
				case "other-scope":
					target.PlatformScopeID = "private:second"
					allowed = mode == storage.SessionModeBackground
				case "cli-owner":
					target.IsCLI, target.Platform, target.PlatformScopeID = true, "cli", "local"
				case "cli-other":
					target.IsCLI, target.ActorID = true, "other"
				case "group":
					target.ConversationKind = contextinfo.ConversationGroup
					allowed = mode != storage.SessionModeBackground
				}
				calls := 0
				f.material = func(context.Context, *storage.Session, *storage.Message) (*branch.PreparedMaterial, error) {
					calls++
					return nil, nil
				}
				row, err := f.s.Fork(t.Context(), target, message.ID)
				if !allowed {
					if err == nil || calls != 0 || f.count(t) != 1 {
						t.Fatalf("unauthorized fork: %+v %v calls=%d", row, err, calls)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				wantMode := mode
				if mode == storage.SessionModeBackground {
					wantMode = storage.SessionModeWork
				}
				if row.Mode != wantMode || row.OwnerID != target.ActorID || row.Platform != target.Platform || row.PlatformScopeID != target.PlatformScopeID || row.ParentSessionID != source.ID || row.ForkFromMessageID != message.ID {
					t.Fatalf("fork: %+v", row)
				}
				if got := f.c.CurrentBinding(target); got == nil || got.SessionID() != row.ID {
					t.Fatal("fork not activated")
				}
				latest, err := f.store.Sessions().Get(t.Context(), source.ID)
				if err != nil || latest.Mode != mode || latest.Metadata != source.Metadata {
					t.Fatalf("source changed: %+v %v", latest, err)
				}
				if strings.Contains(row.Metadata, "source_only") || strings.Contains(row.Metadata, "background_kind") || !strings.Contains(row.Metadata, "llm_origin") {
					t.Fatalf("inheritance: %s", row.Metadata)
				}
			})
		}
	}
}

func TestForkMessageAndToolBoundaries(t *testing.T) {
	f := newFixture(t)
	source := f.source(t, storage.SessionModeWork)
	user := f.message(t, source.ID, storage.RoleUser)
	if _, err := f.s.Fork(t.Context(), scope(), user.ID); err == nil {
		t.Fatal("user message accepted")
	}
	if _, err := f.s.Fork(t.Context(), scope(), " "); err == nil {
		t.Fatal("empty id accepted")
	}
	pending := toolBatch(t, f, source.ID, "pending", true)
	if _, err := f.s.Fork(t.Context(), scope(), pending.Call.ID); err == nil {
		t.Fatal("pending batch accepted")
	}
	sealed := toolBatch(t, f, source.ID, "sealed", false)
	later := f.message(t, source.ID, storage.RoleAssistant)
	row, err := f.s.Fork(t.Context(), scope(), sealed.Call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ForkFromMessageID != sealed.Results[1].ID {
		t.Fatalf("boundary: %+v", row)
	}
	rows, err := f.store.Messages().ListBySessionUpTo(t.Context(), source.ID, row.ForkFromMessageID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == later.ID {
			t.Fatal("included message after boundary")
		}
	}
}

func TestForkRechecksAfterUnlockedPreparation(t *testing.T) {
	for _, change := range []string{"rebind", "switch", "owner", "origin", "delete", "busy", "cancel", "prepare-error", "save", "checkpoint"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			source := f.source(t, storage.SessionModeWork)
			message := f.message(t, source.ID, storage.RoleAssistant)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if change == "save" {
				f.failSave()
			}
			var wantBinding *share.Binding
			wantCount := 0
			f.material = func(context.Context, *storage.Session, *storage.Message) (*branch.PreparedMaterial, error) {
				// 独立 context 取得业务准入，证明材料准备已释放第一阶段租约。
				outside, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				locked, leave, err := f.c.EnterActivation(outside, scope(), source.ID)
				if err != nil {
					t.Fatal(err)
				}
				switch change {
				case "rebind":
					f.c.SetCurrent(locked, scope(), "")
					f.c.SetCurrent(locked, scope(), source.ID)
				case "owner", "origin", "checkpoint":
					_, err = f.store.Sessions().Mutate(locked, source.ID, func(row *storage.Session) error {
						switch change {
						case "owner":
							row.OwnerID = "other"
						case "origin":
							row.Metadata = `{"llm_origin":{"protocol":"response","provider":"changed"}}`
						case "checkpoint":
							row.Metadata = `{"llm_origin":{"protocol":"response","provider":"p"},"llm_checkpoint":"advanced"}`
						}
						return nil
					})
				case "delete":
					err = f.store.Sessions().Delete(locked, source.ID)
				case "busy":
					f.setBusy(source.ID)
				case "cancel":
					cancel()
				}
				leave()
				if err != nil {
					t.Fatal(err)
				}
				if change == "switch" {
					if _, err := f.c.Create(outside, scope(), control.CreateRequest{}); err != nil {
						t.Fatal(err)
					}
				}
				wantBinding, wantCount = f.c.CurrentBinding(scope()), f.count(t)
				if change == "prepare-error" {
					return &branch.PreparedMaterial{Seed: seed()}, fmt.Errorf("prepare failed")
				}
				if change == "checkpoint" {
					return &branch.PreparedMaterial{Seed: seed()}, nil
				}
				return nil, nil
			}
			if _, err := f.s.Fork(ctx, scope(), message.ID); err == nil {
				t.Fatal("stale fork committed")
			}
			if f.count(t) != wantCount || f.c.CurrentBinding(scope()) != wantBinding {
				t.Fatal("failed fork changed storage or binding")
			}
		})
	}
}

func TestForkControlTimerAndRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		source := f.source(t, storage.SessionModeWork)
		original := f.c.CurrentBinding(scope())
		message := f.message(t, source.ID, storage.RoleAssistant)
		if err := f.c.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Second)
		row, err := f.s.Fork(context.Background(), scope(), message.ID)
		if err != nil {
			t.Fatal(err)
		}
		if original.Valid() {
			t.Fatal("source handle retained")
		}
		if sessions, bindings := f.releaseCounts(); sessions != 1 || bindings != 1 {
			t.Fatalf("switch release %d %d", sessions, bindings)
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if got := f.c.CurrentBinding(scope()); got == nil || got.SessionID() != row.ID {
			t.Fatal("new binding inherited old deadline")
		}
		time.Sleep(31 * time.Second)
		synctest.Wait()
		if f.c.CurrentBinding(scope()) != nil {
			t.Fatal("fork bypassed idle tracking")
		}
		if _, err := f.store.Sessions().Get(context.Background(), row.ID); err != nil {
			t.Fatal("expiry deleted history", err)
		}
	})
}
