package session

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/fileops"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type testModels func(context.Context) modelmgr.NamingSelection

func (fn testModels) ResolveNaming(ctx context.Context) modelmgr.NamingSelection { return fn(ctx) }

type testMaterials struct{}

func (testMaterials) MaterialFor(llm.Origin) (MaterialPreparer, error) { return DisplayMaterial{}, nil }

type testTextClient struct {
	llm.Client
	call func(context.Context, llm.TextRequest) (llm.TextResult, error)
}

func (c testTextClient) GenerateText(ctx context.Context, req llm.TextRequest) (llm.TextResult, error) {
	return c.call(ctx, req)
}

func newTestStore(t *testing.T) storage.Store {
	t.Helper()
	store, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}
func newTestService(t *testing.T) (*Supervisor, storage.Store) {
	t.Helper()
	store := newTestStore(t)
	return newTestSupervisor(t, store, nil), store
}

func newTestSupervisor(t *testing.T, store storage.Store, configure func(*config.Config, *SupervisorDeps)) *Supervisor {
	t.Helper()
	cfg := &config.Config{Session: config.SessionConfig{DefaultMode: storage.SessionModeWork, Naming: config.SessionNamingConfig{TriggerStep: 1}}}
	deps := SupervisorDeps{Store: store, Models: testModels(func(context.Context) modelmgr.NamingSelection { return modelmgr.NamingSelection{} }), ActiveSessionIDs: func() []string { return nil }, CheckForeground: func(context.Context, *storage.Session) error { return nil }, Materials: testMaterials{}, IsSuperadmin: func(Scope) bool { return false }, ReleaseSession: func(string) {}, ReleaseBinding: func(*Binding) {}}
	if configure != nil {
		configure(cfg, &deps)
	}
	s, err := NewSupervisor(context.Background(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIdleExpiryCombinesNamingAndExternalRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newTestStore(t)
		var calls atomic.Int32
		var releasedSessions []string
		var releasedBindings []*Binding
		s := newTestSupervisor(t, store, func(cfg *config.Config, deps *SupervisorDeps) {
			cfg.Session.IdleExpiration.PrivateUserTTLMinutes = 1
			deps.ReleaseSession = func(id string) { releasedSessions = append(releasedSessions, id) }
			deps.ReleaseBinding = func(b *Binding) { releasedBindings = append(releasedBindings, b) }
			deps.Models = testModels(func(context.Context) modelmgr.NamingSelection {
				return modelmgr.NamingSelection{Naming: modelmgr.Selection{ModelSelection: config.ModelSelection{Provider: "p", Model: "m"}, Client: testTextClient{call: func(context.Context, llm.TextRequest) (llm.TextResult, error) {
					calls.Add(1)
					return llm.TextResult{}, errors.New("model failed")
				}}}}
			})
		})
		scope := Scope{ActorID: "u", Platform: "test", PlatformScopeID: "private", ConversationKind: contextinfo.ConversationPrivate}
		row, err := s.Create(t.Context(), scope, CreateRequest{Title: "original"})
		if err != nil {
			t.Fatal(err)
		}
		_, binding, err := s.CurrentBound(t.Context(), scope)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Messages().Append(t.Context(), &storage.Message{SessionID: row.ID, Role: storage.RoleUser, Content: "fallback"}); err != nil {
			t.Fatal(err)
		}
		s.MaybeScheduleNaming(t.Context(), row.ID)
		synctest.Wait()
		time.Sleep(61 * time.Second)
		synctest.Wait()
		if binding.Valid() || len(releasedSessions) != 1 || releasedSessions[0] != row.ID || len(releasedBindings) != 1 || releasedBindings[0] != binding {
			t.Fatalf("release did not preserve identity: %v %v", releasedSessions, releasedBindings)
		}
		if _, err := s.Resume(t.Context(), scope, row.ID); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			s.MaybeScheduleNaming(t.Context(), row.ID)
			synctest.Wait()
		}
		stored, _ := store.Sessions().Get(t.Context(), row.ID)
		if stored.Title != "original" || calls.Load() != 3 {
			t.Fatalf("Naming cache survived expiry: row=%+v calls=%d", stored, calls.Load())
		}
		s.MaybeScheduleNaming(t.Context(), row.ID)
		synctest.Wait()
		stored, _ = store.Sessions().Get(t.Context(), row.ID)
		if stored.Title != "fallback" || calls.Load() != 4 {
			t.Fatalf("retry=%+v calls=%d", stored, calls.Load())
		}
	})
}

func TestPublicBindingIdentityAndSynchronousRollbackRelease(t *testing.T) {
	for _, action := range []string{"reset", "create", "resume", "fork", "delete", "cleanup", "missing"} {
		t.Run(action, func(t *testing.T) {
			store := newTestStore(t)
			rollback := fileops.NewRollbackManager()
			var released *Binding
			s := newTestSupervisor(t, store, func(_ *config.Config, deps *SupervisorDeps) {
				deps.ReleaseBinding = func(b *Binding) { released = b; rollback.Forget(b) }
			})
			scope := Scope{ActorID: "u", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
			row, err := s.Create(t.Context(), scope, CreateRequest{})
			if err != nil {
				t.Fatal(err)
			}
			_, binding, err := s.CurrentBound(t.Context(), scope)
			if err != nil {
				t.Fatal(err)
			}
			lease, ok := rollback.Session(binding)
			if !ok {
				t.Fatal("binding rejected")
			}
			ctx := WithBinding(contextinfo.WithExecution(t.Context(), contextinfo.Execution{RunID: "run"}), binding)
			if got, _ := BindingFromContext(ctx); got != binding {
				t.Fatal("context copied binding")
			}
			facts, _ := contextinfo.ExecutionFromContext(WithBinding(ctx, nil))
			if facts.SessionID != "" || facts.RunID != "run" {
				t.Fatalf("clear facts=%+v", facts)
			}
			c, err := s.ObserveBindingChanged(func(ctx context.Context, e BindingChangedEvent) error {
				if e.Old != binding {
					t.Error("event copied old binding")
				}
				if released != binding {
					t.Error("business release depended on notification")
				}
				if e.New != nil {
					_, current, err := s.CurrentBound(ctx, scope)
					if err != nil || current != e.New {
						t.Error("event copied new binding")
					}
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Disconnect()
			switch action {
			case "reset":
				err = s.ResetCurrent(t.Context(), scope)
			case "create":
				_, err = s.Create(t.Context(), scope, CreateRequest{})
			case "resume":
				next := &storage.Session{OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID, Mode: storage.SessionModeWork}
				err = store.Sessions().Create(t.Context(), next)
				if err == nil {
					_, err = s.Resume(t.Context(), scope, next.ID)
				}
			case "fork":
				message := &storage.Message{SessionID: row.ID, Role: storage.RoleAssistant, Content: "answer"}
				err = store.Messages().Append(t.Context(), message)
				if err == nil {
					_, err = s.Fork(t.Context(), scope, message.ID)
				}
			case "delete":
				err = s.Delete(t.Context(), scope, row.ID)
			case "cleanup":
				_, err = store.Sessions().Mutate(t.Context(), row.ID, func(row *storage.Session) error { row.UpdatedAt = storage.Now().Add(-2 * time.Hour); return nil })
				if err == nil {
					_, err = s.CleanupExpired(t.Context(), storage.Now().Add(-time.Hour))
				}
			case "missing":
				err = store.Sessions().Delete(t.Context(), row.ID)
				if err == nil {
					_, missing := s.Current(t.Context(), scope)
					if !errors.Is(missing, storage.ErrNotFound) {
						t.Fatal(missing)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lease.List(); !errors.Is(err, fileops.ErrRollbackExpired) {
				t.Fatalf("rollback survived %s: %v", action, err)
			}
		})
	}
}

func TestPromotionObserverErrorPreservesResultAndBinding(t *testing.T) {
	s, store := newTestService(t)
	scope := Scope{ActorID: "u", IsCLI: true}
	row, err := s.PrepareBackground(t.Context(), scope, BackgroundRequest{Kind: "cron"})
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("observer failed")
	var observed *Binding
	c, err := s.ObserveForegroundActivated(func(_ context.Context, e ForegroundActivatedEvent) error { observed = e.Binding; return want }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()
	got, err := s.Resume(t.Context(), scope, row.ID)
	if !errors.Is(err, want) || got == nil || IsBackground(got) {
		t.Fatalf("promotion=%+v %v", got, err)
	}
	_, current, err := s.CurrentBound(t.Context(), scope)
	if err != nil || current != observed {
		t.Fatal("promotion binding copied")
	}
	stored, err := store.Sessions().Get(t.Context(), row.ID)
	if err != nil || IsBackground(stored) {
		t.Fatalf("commit=%+v %v", stored, err)
	}
}

type partialMaterial struct {
	prepared *PreparedMaterial
	err      error
}

func (p partialMaterial) PrepareFork(context.Context, *storage.Session, *storage.Message) (*PreparedMaterial, error) {
	return p.prepared, p.err
}
func (p partialMaterial) PrepareCopy(context.Context, *storage.Session) (*PreparedMaterial, error) {
	return p.prepared, p.err
}

func TestMaterialAdapterPreservesNilAndPartialFailure(t *testing.T) {
	want := errors.New("partial material")
	for _, prepared := range []*PreparedMaterial{nil, {ExpectedCheckpointID: "checkpoint"}} {
		p := materialPreparer{source: partialMaterial{prepared, want}}
		fork, forkErr := p.PrepareFork(t.Context(), nil, nil)
		copy, copyErr := p.PrepareCopy(t.Context(), nil)
		if !errors.Is(forkErr, want) || !errors.Is(copyErr, want) {
			t.Fatal("lost material error")
		}
		if prepared == nil {
			if fork != nil || copy != nil {
				t.Fatal("invented nil material")
			}
		} else if fork.ExpectedCheckpointID != prepared.ExpectedCheckpointID || copy.ExpectedCheckpointID != prepared.ExpectedCheckpointID {
			t.Fatal("lost partial material")
		}
	}
}

func TestCloseWaitsNamingBeforeClosingBinding(t *testing.T) {
	store := newTestStore(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	s := newTestSupervisor(t, store, func(_ *config.Config, deps *SupervisorDeps) {
		deps.Models = testModels(func(context.Context) modelmgr.NamingSelection {
			return modelmgr.NamingSelection{Naming: modelmgr.Selection{ModelSelection: config.ModelSelection{Model: "title"}, Client: testTextClient{call: func(context.Context, llm.TextRequest) (llm.TextResult, error) {
				close(started)
				<-release
				return llm.TextResult{Text: "late"}, nil
			}}}}
		})
	})
	t.Cleanup(unblock)
	scope := Scope{ActorID: "u", IsCLI: true}
	row, err := s.Create(t.Context(), scope, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Messages().Append(t.Context(), &storage.Message{SessionID: row.ID, Role: storage.RoleUser, Content: "user"}); err != nil {
		t.Fatal(err)
	}
	s.MaybeScheduleNaming(t.Context(), row.ID)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker not started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close=%v", err)
	}
	c, err := s.ObserveBindingChanged(func(context.Context, BindingChangedEvent) error { return nil }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal("binding closed before naming", err)
	}
	c.Disconnect()
	select {
	case <-s.Done():
		t.Fatal("root Done premature")
	default:
	}
	unblock()
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err == nil {
		t.Fatal("closed supervisor restarted")
	}
	if _, err := store.Sessions().Get(t.Context(), row.ID); err != nil {
		t.Fatal("borrowed store was closed", err)
	}
}
