package control

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/session/binding"
	"elbot/internal/session/share"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type fixture struct {
	s                *Service
	bindings         *binding.Manager
	store            *sqlite.Store
	mu               sync.Mutex
	busy             []string
	releasedSessions []string
	releasedBindings []*share.Binding
}

func newFixture(t *testing.T, ttl IdleExpirationConfig) *fixture {
	t.Helper()
	store, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{store: store, bindings: binding.NewManager()}
	f.s = NewService(Config{DefaultMode: storage.SessionModeWork, IdleExpiration: ttl}, ServiceDeps{
		Sessions: store.Sessions(), Messages: store.Messages(), Bindings: f.bindings,
		ActiveSessionIDs: func() []string { f.mu.Lock(); defer f.mu.Unlock(); return append([]string(nil), f.busy...) },
		CheckForeground:  func(context.Context, *storage.Session) error { return nil },
		IsSuperadmin:     func(scope share.Scope) bool { return scope.ActorID == "admin" },
		ReleaseSession:   func(id string) { f.mu.Lock(); defer f.mu.Unlock(); f.releasedSessions = append(f.releasedSessions, id) },
		ReleaseBinding: func(b *share.Binding) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.releasedBindings = append(f.releasedBindings, b)
		},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := f.s.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := f.bindings.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func privateScope() share.Scope {
	return share.Scope{ActorID: "u", Platform: "test", PlatformScopeID: "private:u", ConversationKind: contextinfo.ConversationPrivate}
}

func (f *fixture) create(t *testing.T, scope share.Scope, id string) *storage.Session {
	t.Helper()
	row, err := f.s.Create(context.Background(), scope, CreateRequest{ID: id, Title: "test", Metadata: `{"keep":{"number":7}}`})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func (f *fixture) setBusy(ids ...string) { f.mu.Lock(); defer f.mu.Unlock(); f.busy = ids }

func (f *fixture) releaseCounts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.releasedSessions), len(f.releasedBindings)
}

func TestCurrentModesManagementAndQueries(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	row, err := f.s.GetOrCreateCurrent(ctx, scope, " first message ")
	if err != nil || row.Title != "first message" || row.Mode != storage.SessionModeWork {
		t.Fatalf("create: %+v %v", row, err)
	}
	original := f.s.CurrentBinding(scope)
	if _, err := f.s.GetOrCreateCurrent(ctx, scope, "ignored"); err != nil {
		t.Fatal(err)
	}
	if f.s.CurrentBinding(scope) != original {
		t.Fatal("current activation changed")
	}
	result, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeChat})
	if err != nil || result.Session.Mode != storage.SessionModeChat {
		t.Fatalf("mode: %+v %v", result, err)
	}
	if _, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeWork}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []storage.Message{{SessionID: row.ID, Role: storage.RoleUser, Content: "question"}, {SessionID: row.ID, Role: storage.RoleAssistant, Content: "answer"}} {
		if err := f.store.Messages().Append(ctx, &message); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeChat}); !errors.Is(err, ErrChatModeRequiresEmptySession) {
		t.Fatalf("nonempty chat: %v", err)
	}
	status, err := f.s.Status(ctx, scope)
	if err != nil || status.MessageCount != 2 || status.LastUserContent != "question" || status.LastAnswerContent != "answer" {
		t.Fatalf("status: %+v %v", status, err)
	}
	if _, err := f.s.Rename(ctx, scope, "", " renamed "); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pin(ctx, scope, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Archive(ctx, scope, row.ID); err != nil {
		t.Fatal(err)
	}
	listed, err := f.s.ListPage(ctx, scope, "renamed", 10, 0, true)
	if err != nil || len(listed) != 1 {
		t.Fatalf("archive list: %+v %v", listed, err)
	}
	if err := f.s.ResetCurrent(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if original.Valid() {
		t.Fatal("reset retained binding")
	}
	resumed, err := f.s.Unarchive(ctx, scope, row.ID)
	if err != nil || resumed.ArchivedAt != nil || resumed.PinnedAt == nil {
		t.Fatalf("unarchive: %+v %v", resumed, err)
	}
	if _, err := f.s.Unpin(ctx, scope, row.ID); err != nil {
		t.Fatal(err)
	}
	other := f.create(t, scope, "other")
	resumable, err := f.s.ListResumablePage(ctx, scope, 10, 0)
	if err != nil || len(resumable) != 1 || resumable[0].ID != row.ID {
		t.Fatalf("resumable: %+v %v", resumable, err)
	}
	f.setBusy(other.ID)
	if err := f.s.ResetCurrent(ctx, scope); !errors.Is(err, share.ErrSessionBusy) {
		t.Fatalf("busy reset: %v", err)
	}
	if err := f.s.Delete(ctx, scope, other.ID); !errors.Is(err, share.ErrSessionBusy) {
		t.Fatalf("busy delete: %v", err)
	}
	if _, err := f.s.Resume(ctx, scope, row.ID); !errors.Is(err, share.ErrSessionBusy) {
		t.Fatalf("busy switch: %v", err)
	}
	f.setBusy()
	if err := f.s.Delete(ctx, scope, other.ID); err != nil {
		t.Fatal(err)
	}
	if f.s.CurrentBinding(scope) != nil {
		t.Fatal("deleted binding retained")
	}
	if _, err := f.store.Sessions().Get(ctx, other.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("delete: %v", err)
	}
}

func TestAccessAndCreateUnderPreallocatedAdmission(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	row := f.create(t, scope, "z-source")
	other := scope
	other.ActorID = "other"
	if _, err := f.s.Resume(ctx, other, row.ID); err == nil {
		t.Fatal("cross-user resume allowed")
	}
	if _, err := f.s.Rename(ctx, other, row.ID, "bad"); err == nil {
		t.Fatal("cross-user rename allowed")
	}
	if err := f.s.Delete(ctx, other, row.ID); err == nil {
		t.Fatal("cross-user delete allowed")
	}
	locked, release, err := f.s.EnterActivation(ctx, scope, "a-target")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Create(locked, scope, CreateRequest{ID: "a-target"})
	release()
	if err != nil {
		t.Fatalf("preallocated create: %v", err)
	}
	// 当前记录被外部删除时，读操作移除悬挂绑定，下一次输入创建新记录。
	if err := f.store.Sessions().Delete(ctx, "a-target"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Current(ctx, scope); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing current: %v", err)
	}
	if f.s.CurrentBinding(scope) != nil {
		t.Fatal("missing row retained current")
	}
	if _, err := f.s.GetOrCreateCurrent(ctx, scope, "fresh"); err != nil {
		t.Fatal(err)
	}
}

func TestActivateModeCreatesAndReusesSession(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	if _, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{}); err == nil {
		t.Fatal("empty mode accepted without a current session")
	}
	if f.s.CurrentBinding(scope) != nil {
		t.Fatal("invalid mode created a binding")
	}
	created, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeChat, NewSessionTitle: "chat"})
	if err != nil || created.AlreadyActive || created.Session.Mode != storage.SessionModeChat || created.Session.Title != "chat" {
		t.Fatalf("mode creation: %+v %v", created, err)
	}
	pristine := f.s.CurrentBinding(scope)
	again, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeChat})
	if err != nil || !again.AlreadyActive || again.Session.ID != created.Session.ID || f.s.CurrentBinding(scope) != pristine {
		t.Fatalf("mode reuse: %+v %v", again, err)
	}
	for _, mode := range []string{"", "invalid"} {
		if _, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: mode}); err == nil {
			t.Fatalf("invalid mode %q accepted", mode)
		}
	}
	if err := f.store.Sessions().Delete(ctx, created.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{}); err == nil {
		t.Fatal("empty mode accepted with a missing current row")
	}
	fresh, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeWork})
	if err != nil || fresh.Session.ID == created.Session.ID || pristine.Valid() {
		t.Fatalf("missing current mode creation: %+v %v", fresh, err)
	}
}

func TestCreateResolvesAndValidatesMode(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	f.s.cfg.DefaultMode = storage.SessionModeChat
	row, err := f.s.Create(ctx, scope, CreateRequest{})
	if err != nil || row.Mode != storage.SessionModeChat {
		t.Fatalf("default mode: %+v %v", row, err)
	}
	row, err = f.s.Create(ctx, scope, CreateRequest{Mode: storage.SessionModeWork})
	if err != nil || row.Mode != storage.SessionModeWork {
		t.Fatalf("explicit mode: %+v %v", row, err)
	}
	original := f.s.CurrentBinding(scope)
	if _, err := f.s.Create(ctx, scope, CreateRequest{Mode: "invalid"}); err == nil {
		t.Fatal("invalid creation mode accepted")
	}
	if f.s.CurrentBinding(scope) != original || !original.Valid() {
		t.Fatal("invalid creation changed current binding")
	}
}

type failingSessions struct {
	storage.SessionRepository
	createErr, deleteErr error
}

func (s failingSessions) Create(ctx context.Context, row *storage.Session) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.SessionRepository.Create(ctx, row)
}
func (s failingSessions) Delete(ctx context.Context, id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.SessionRepository.Delete(ctx, id)
}

func TestStorageFailureDoesNotRetireBinding(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	row := f.create(t, scope, "old")
	b := f.s.CurrentBinding(scope)
	failure := errors.New("storage failed")
	f.s.deps.Sessions = failingSessions{SessionRepository: f.store.Sessions(), createErr: failure, deleteErr: failure}
	if _, err := f.s.Create(ctx, scope, CreateRequest{ID: "new"}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := f.s.Delete(ctx, scope, row.ID); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if f.s.CurrentBinding(scope) != b || !b.Valid() {
		t.Fatal("storage failure changed binding")
	}
	if sessions, bindings := f.releaseCounts(); sessions != 0 || bindings != 0 {
		t.Fatal("storage failure released state")
	}
}
