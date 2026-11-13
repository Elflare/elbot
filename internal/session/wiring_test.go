package session

import (
	"context"
	"errors"
	"fmt"
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
	"elbot/internal/operationresult"
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

func (c testTextClient) RetryOptions() llm.RetryOptions { return llm.RetryOptions{} }

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
	deps := SupervisorDeps{AdoptForeground: func(context.Context, *storage.Session, *Binding) {}, Store: store, Models: testModels(func(context.Context) modelmgr.NamingSelection { return modelmgr.NamingSelection{} }), ActiveSessionIDs: func() []string { return nil }, CheckForeground: func(context.Context, *storage.Session) (operationresult.Result[struct{}], error) {
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}, Materials: testMaterials{}, IsSuperadmin: func(Scope) bool { return false }, ReleaseSession: func(string) {}, ReleaseBinding: func(*Binding) {}}
	deps.SessionIDCode = func(name string, kind contextinfo.ChatType) (string, error) {
		codes, ok := map[string][2]string{"cli": {"cl", "cl"}, "qqonebot": {"ob", "OB"}, "qqofficial": {"qq", "QQ"}, "telegram": {"tg", "TG"}}[name]
		if !ok {
			return "", fmt.Errorf("unknown test platform %q", name)
		}
		if kind == contextinfo.ChatGroup || kind == contextinfo.ChatChannel {
			return codes[1], nil
		}
		return codes[0], nil
	}
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
		scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "qqonebot", ID: "u"}, Platform: "qqonebot", ChatID: "private", ChatType: contextinfo.ChatPrivate}
		created, err := s.Create(t.Context(), scope, CreateRequest{Title: "original"})
		if err != nil || created.Status != operationresult.Success {
			t.Fatalf("create: %+v %v", created, err)
		}
		row := created.Data
		current, err := s.CurrentBound(t.Context(), scope)
		if err != nil || current.Status != operationresult.Success {
			t.Fatalf("current: %+v %v", current, err)
		}
		binding := current.Data.Binding
		if err := store.Messages().Append(t.Context(), &storage.Message{ID: storage.NewID(), SessionID: row.ID, Role: storage.RoleUser, Content: "fallback"}); err != nil {
			t.Fatal(err)
		}
		s.MaybeScheduleNaming(t.Context(), row.ID)
		synctest.Wait()
		time.Sleep(61 * time.Second)
		synctest.Wait()
		if binding.Valid() || len(releasedSessions) != 1 || releasedSessions[0] != row.ID || len(releasedBindings) != 1 || releasedBindings[0] != binding {
			t.Fatalf("release did not preserve identity: %v %v", releasedSessions, releasedBindings)
		}
		if result, err := s.Resume(t.Context(), scope, row.ID); err != nil || result.Status != operationresult.Success {
			t.Fatalf("resume: %+v %v", result, err)
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
			scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u"}, Platform: "cli", ChatID: "local", ChatType: "private"}
			created, err := s.Create(t.Context(), scope, CreateRequest{})
			if err != nil || created.Status != operationresult.Success {
				t.Fatalf("create: %+v %v", created, err)
			}
			row := created.Data
			currentResult, err := s.CurrentBound(t.Context(), scope)
			if err != nil || currentResult.Status != operationresult.Success {
				t.Fatalf("current: %+v %v", currentResult, err)
			}
			binding := currentResult.Data.Binding
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
			observed := make(chan struct{})
			c, err := s.ObserveBindingChanged(func(ctx context.Context, e BindingChangedEvent) {
				defer close(observed)
				if e.Old != binding {
					t.Error("event copied old binding")
				}
				if released != binding {
					t.Error("business release depended on notification")
				}
				if e.New != nil {
					current, err := s.CurrentBound(ctx, scope)
					if err != nil || current.Status != operationresult.Success || current.Data.Binding != e.New {
						t.Error("event copied new binding")
					}
				}

			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Disconnect()
			switch action {
			case "reset":
				result, resetErr := s.ResetCurrent(t.Context(), scope)
				err = resetErr
				if err == nil && result.Status != operationresult.Success {
					err = fmt.Errorf("reset: %s", result.Message)
				}
			case "create":
				result, createErr := s.Create(t.Context(), scope, CreateRequest{})
				err = createErr
				if err == nil && result.Status != operationresult.Success {
					err = fmt.Errorf("create: %s", result.Message)
				}
			case "resume":
				next := &storage.Session{ID: storage.NewID(), Owner: scope.Owner, Platform: scope.Platform, ChatID: scope.ChatID, Mode: storage.SessionModeWork, ChatType: scope.ChatType}
				err = store.Sessions().Create(t.Context(), next)
				if err == nil {
					result, resumeErr := s.Resume(t.Context(), scope, next.ID)
					err = resumeErr
					if err == nil && result.Status != operationresult.Success {
						err = fmt.Errorf("resume: %s", result.Message)
					}
				}
			case "fork":
				message := &storage.Message{ID: storage.NewID(), SessionID: row.ID, Role: storage.RoleAssistant, Content: "answer"}
				err = store.Messages().Append(t.Context(), message)
				if err == nil {
					result, forkErr := s.Fork(t.Context(), scope, message.ID)
					err = forkErr
					if err == nil && result.Status != operationresult.Success {
						err = fmt.Errorf("fork: %s", result.Message)
					}
				}
			case "delete":
				result, deleteErr := s.Delete(t.Context(), scope, row.ID)
				err = deleteErr
				if err == nil && result.Status != operationresult.Success {
					err = fmt.Errorf("delete: %s", result.Message)
				}
			case "cleanup":
				_, err = store.Sessions().Mutate(t.Context(), row.ID, func(row *storage.Session) error { row.UpdatedAt = storage.Now().Add(-2 * time.Hour); return nil })
				if err == nil {
					_, err = s.CleanupExpired(t.Context(), storage.Now().Add(-time.Hour))
				}
			case "missing":
				err = store.Sessions().Delete(t.Context(), row.ID)
				if err == nil {
					missing, readErr := s.Current(t.Context(), scope)
					if readErr != nil || missing.Status != operationresult.NoCurrentSession {
						t.Fatalf("missing current: %+v %v", missing, readErr)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lease.List(); !errors.Is(err, fileops.ErrRollbackExpired) {
				t.Fatalf("rollback survived %s: %v", action, err)
			}
			select {
			case <-observed:
			case <-time.After(time.Second):
				t.Fatal("binding notification did not finish")
			}
		})
	}
}

func TestPromotionCallbackPreservesResultAndBinding(t *testing.T) {
	store := newTestStore(t)
	var observed *Binding
	s := newTestSupervisor(t, store, func(_ *config.Config, deps *SupervisorDeps) {
		deps.AdoptForeground = func(_ context.Context, _ *storage.Session, b *Binding) { observed = b }
	})
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u"}, Platform: "cli", IsSuperadmin: true}
	prepared, err := s.PrepareBackground(t.Context(), scope, BackgroundRequest{TaskType: "cron"})
	if err != nil || prepared.Status != operationresult.Success {
		t.Fatalf("prepare background: %+v %v", prepared, err)
	}
	row := prepared.Data
	got, err := s.Resume(t.Context(), scope, row.ID)
	if err != nil || got.Status != operationresult.Success || got.Data == nil || IsBackground(got.Data) {
		t.Fatalf("promotion=%+v %v", got, err)
	}
	current, err := s.CurrentBound(t.Context(), scope)
	if err != nil || current.Status != operationresult.Success || current.Data.Binding != observed {
		t.Fatal("promotion binding copied")
	}
	stored, err := store.Sessions().Get(t.Context(), row.ID)
	if err != nil || IsBackground(stored) {
		t.Fatalf("commit=%+v %v", stored, err)
	}
}

type partialMaterial struct {
	result operationresult.Result[*PreparedMaterial]
	err    error
}

func (p partialMaterial) PrepareFork(context.Context, *storage.Session, *storage.Message) (operationresult.Result[*PreparedMaterial], error) {
	return p.result, p.err
}
func (p partialMaterial) PrepareCopy(context.Context, *storage.Session) (operationresult.Result[*PreparedMaterial], error) {
	return p.result, p.err
}

func TestMaterialAdapterDropsPartialDataOnErrorAndKeepsEmptySuccess(t *testing.T) {
	want := errors.New("material failed")
	partial := &PreparedMaterial{ExpectedCheckpointID: "checkpoint"}
	p := materialPreparer{source: partialMaterial{result: operationresult.Result[*PreparedMaterial]{Status: operationresult.Success, Data: partial}, err: want}}
	fork, forkErr := p.PrepareFork(t.Context(), nil, nil)
	copy, copyErr := p.PrepareCopy(t.Context(), nil)
	if !errors.Is(forkErr, want) || !errors.Is(copyErr, want) || fork.Data != nil || copy.Data != nil {
		t.Fatalf("partial material escaped: fork=%+v/%v copy=%+v/%v", fork, forkErr, copy, copyErr)
	}
	p.source = partialMaterial{result: operationresult.Result[*PreparedMaterial]{Status: operationresult.Success}}
	fork, forkErr = p.PrepareFork(t.Context(), nil, nil)
	copy, copyErr = p.PrepareCopy(t.Context(), nil)
	if forkErr != nil || copyErr != nil || fork.Status != operationresult.Success || copy.Status != operationresult.Success || fork.Data != nil || copy.Data != nil {
		t.Fatalf("empty success changed: fork=%+v/%v copy=%+v/%v", fork, forkErr, copy, copyErr)
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
	scope := Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u"}, Platform: "cli"}
	created, err := s.Create(t.Context(), scope, CreateRequest{})
	if err != nil || created.Status != operationresult.Success {
		t.Fatalf("create: %+v %v", created, err)
	}
	row := created.Data
	if err := store.Messages().Append(t.Context(), &storage.Message{ID: storage.NewID(), SessionID: row.ID, Role: storage.RoleUser, Content: "user"}); err != nil {
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
	c, err := s.ObserveBindingChanged(func(context.Context, BindingChangedEvent) {}, signal.ConnectOptions{})
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
