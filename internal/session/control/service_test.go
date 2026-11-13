package control

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
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
		AllocateID: func(ctx context.Context, _ share.IDOrigin) (string, error) { return storage.NewID(), ctx.Err() },
		Sessions:   store.Sessions(), Messages: store.Messages(), Bindings: f.bindings,
		ActiveSessionIDs: func() []string { f.mu.Lock(); defer f.mu.Unlock(); return append([]string(nil), f.busy...) },
		CheckForeground: func(context.Context, *storage.Session) (operationresult.Result[struct{}], error) {
			return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
		},
		AdoptForeground: func(context.Context, *storage.Session, *share.Binding) {},
		IsSuperadmin:    func(scope share.Scope) bool { return scope.Owner.ID == "admin" },
		ReleaseSession:  func(id string) { f.mu.Lock(); defer f.mu.Unlock(); f.releasedSessions = append(f.releasedSessions, id) },
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
	return share.Scope{Owner: contextinfo.ActorIdentity{Platform: "test", ID: "u"}, Platform: "test", ChatID: "u", ChatType: contextinfo.ChatPrivate}
}

func (f *fixture) create(t *testing.T, scope share.Scope, id string) *storage.Session {
	t.Helper()
	allocate := f.s.deps.AllocateID
	f.s.deps.AllocateID = func(ctx context.Context, _ share.IDOrigin) (string, error) { return id, ctx.Err() }
	defer func() { f.s.deps.AllocateID = allocate }()
	result, err := f.s.Create(context.Background(), scope, CreateRequest{Title: "test", Metadata: `{"keep":{"number":7}}`})
	if err != nil || result.Status != operationresult.Success {
		t.Fatalf("create: %+v %v", result, err)
	}
	return result.Data
}

func (f *fixture) setBusy(ids ...string) { f.mu.Lock(); defer f.mu.Unlock(); f.busy = ids }

func (f *fixture) releaseCounts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.releasedSessions), len(f.releasedBindings)
}

func TestResumeLastUsesRecentAccessibleSession(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := t.Context(), privateScope()
	if result, err := f.s.ResumeLast(ctx, scope, false); err != nil || result.Status != operationresult.NotFound {
		t.Fatalf("empty last: %+v %v", result, err)
	}
	pinned := f.create(t, scope, "pinned")
	recent := f.create(t, scope, "recent")
	current := f.create(t, scope, "current")
	otherScope := scope
	otherScope.Owner.ID = "other"
	f.create(t, otherScope, "foreign")
	now := storage.Now()
	for i, row := range []*storage.Session{pinned, recent} {
		if _, err := f.store.Sessions().Mutate(ctx, row.ID, func(row *storage.Session) error {
			row.UpdatedAt = now.Add(time.Duration(i-3) * time.Hour)
			if row.ID == pinned.ID {
				row.PinnedAt = &now
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.setBusy(current.ID)
	if result, err := f.s.ResumeLast(ctx, scope, false); err != nil || result.Status != operationresult.Busy {
		t.Fatalf("busy last: %+v %v", result, err)
	}
	f.setBusy()
	for _, want := range []string{recent.ID, current.ID, recent.ID} {
		result, err := f.s.ResumeLast(ctx, scope, false)
		if err != nil || result.Status != operationresult.Success || result.Data.ID != want {
			t.Fatalf("last=%+v err=%v want=%s", result, err, want)
		}
	}
	// 新活动改变候选；不维护固定的两会话切换历史。
	if result, err := f.s.Rename(ctx, scope, pinned.ID, "active now"); err != nil || result.Status != operationresult.Success {
		t.Fatalf("rename: %+v %v", result, err)
	}
	result, err := f.s.ResumeLast(ctx, scope, false)
	if err != nil || result.Status != operationresult.Success || result.Data.ID != pinned.ID {
		t.Fatalf("updated last=%+v err=%v", result, err)
	}
	if result, err := f.s.Archive(ctx, scope, recent.ID); err != nil || result.Status != operationresult.Success {
		t.Fatalf("archive: %+v %v", result, err)
	}
	if result, err := f.s.ResetCurrent(ctx, scope); err != nil || result.Status != operationresult.Success {
		t.Fatalf("reset: %+v %v", result, err)
	}
	result, err = f.s.ResumeLast(ctx, scope, false)
	if err != nil || result.Status != operationresult.Success || result.Data.ID != pinned.ID {
		t.Fatalf("unbound last=%+v err=%v", result, err)
	}
}

func TestCurrentModesManagementAndQueries(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	created, err := f.s.GetOrCreateCurrent(ctx, scope, " first message ")
	if err != nil || created.Status != operationresult.Success || created.Data.Title != "first message" || created.Data.Mode != storage.SessionModeWork {
		t.Fatalf("create: %+v %v", created, err)
	}
	row := created.Data
	metadata, err := storage.DecodeSessionMetadata(row.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"workspace_dir", "user_workspace_dir", "workspace_agent_to_user", "workspace_user_to_agent", "workspace_agent_notice_dirs"} {
		if _, ok := metadata[key]; ok {
			t.Fatalf("new session initialized workspace metadata field %q", key)
		}
	}
	original := f.s.CurrentBinding(scope)
	if result, err := f.s.GetOrCreateCurrent(ctx, scope, "ignored"); err != nil || result.Status != operationresult.Success {
		t.Fatalf("get or create current: %+v %v", result, err)
	}
	if f.s.CurrentBinding(scope) != original {
		t.Fatal("current activation changed")
	}
	mode, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeChat})
	if err != nil || mode.Status != operationresult.Success || mode.Data.Session.Mode != storage.SessionModeChat {
		t.Fatalf("mode: %+v %v", mode, err)
	}
	if result, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeWork}); err != nil || result.Status != operationresult.Success {
		t.Fatalf("mode: %+v %v", result, err)
	}
	for _, message := range []storage.Message{{ID: "question", SessionID: row.ID, Role: storage.RoleUser, Content: "question"}, {ID: "answer", SessionID: row.ID, Role: storage.RoleAssistant, Content: "answer"}} {
		if err := f.store.Messages().Append(ctx, &message); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeChat}); err != nil || result.Status != operationresult.Conflict {
		t.Fatalf("nonempty chat: %+v %v", result, err)
	}
	status, err := f.s.Status(ctx, scope)
	if err != nil || status.Status != operationresult.Success || status.Data.MessageCount != 2 || status.Data.LastUserContent != "question" || status.Data.LastAnswerContent != "answer" {
		t.Fatalf("status: %+v %v", status, err)
	}
	if result, err := f.s.Rename(ctx, scope, "", " renamed "); err != nil || result.Status != operationresult.Success {
		t.Fatalf("rename: %+v %v", result, err)
	}
	if result, err := f.s.Pin(ctx, scope, row.ID); err != nil || result.Status != operationresult.Success {
		t.Fatalf("pin: %+v %v", result, err)
	}
	if result, err := f.s.Archive(ctx, scope, row.ID); err != nil || result.Status != operationresult.Success {
		t.Fatalf("archive: %+v %v", result, err)
	}
	listed, err := f.s.ListPage(ctx, scope, ListRequest{Query: "renamed", PageSize: 10, ArchivedOnly: true})
	if err != nil || listed.Status != operationresult.Success || len(listed.Data.Items) != 1 {
		t.Fatalf("archive list: %+v %v", listed, err)
	}
	if result, err := f.s.ResetCurrent(ctx, scope); err != nil || result.Status != operationresult.Success {
		t.Fatalf("reset: %+v %v", result, err)
	}
	if original.Valid() {
		t.Fatal("reset retained binding")
	}
	resumed, err := f.s.Unarchive(ctx, scope, row.ID)
	if err != nil || resumed.Status != operationresult.Success || resumed.Data.ArchivedAt != nil || resumed.Data.PinnedAt == nil {
		t.Fatalf("unarchive: %+v %v", resumed, err)
	}
	if result, err := f.s.Unpin(ctx, scope, row.ID); err != nil || result.Status != operationresult.Success {
		t.Fatalf("unpin: %+v %v", result, err)
	}
	other := f.create(t, scope, "other")
	resumable, err := f.s.ListPage(ctx, scope, ListRequest{PageSize: 10, ExcludeCurrent: true})
	if err != nil || resumable.Status != operationresult.Success || len(resumable.Data.Items) != 1 || resumable.Data.Items[0].ID != row.ID {
		t.Fatalf("resumable: %+v %v", resumable, err)
	}
	f.setBusy(other.ID)
	if result, err := f.s.ResetCurrent(ctx, scope); err != nil || result.Status != operationresult.Busy {
		t.Fatalf("busy reset: %+v %v", result, err)
	}
	if result, err := f.s.Delete(ctx, scope, other.ID); err != nil || result.Status != operationresult.Busy {
		t.Fatalf("busy delete: %+v %v", result, err)
	}
	if result, err := f.s.Resume(ctx, scope, row.ID); err != nil || result.Status != operationresult.Busy {
		t.Fatalf("busy switch: %+v %v", result, err)
	}
	f.setBusy()
	if result, err := f.s.Delete(ctx, scope, other.ID); err != nil || result.Status != operationresult.Success {
		t.Fatalf("delete: %+v %v", result, err)
	}
	if f.s.CurrentBinding(scope) != nil {
		t.Fatal("deleted binding retained")
	}
	if _, err := f.store.Sessions().Get(ctx, other.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("delete: %v", err)
	}
}

func TestAccessAndCreateUnderScopeAdmission(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	row := f.create(t, scope, "z-source")
	other := scope
	other.Owner.ID = "other"
	if result, err := f.s.Resume(ctx, other, row.ID); err != nil || result.Status != operationresult.Forbidden {
		t.Fatal("cross-user resume allowed")
	}
	if result, err := f.s.Rename(ctx, other, row.ID, "bad"); err != nil || result.Status != operationresult.Forbidden {
		t.Fatal("cross-user rename allowed")
	}
	if result, err := f.s.Delete(ctx, other, row.ID); err != nil || result.Status != operationresult.Forbidden {
		t.Fatal("cross-user delete allowed")
	}
	locked, release, err := f.bindings.EnterScope(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	created, err := f.s.Create(locked, scope, CreateRequest{})
	release()
	if err != nil || created.Status != operationresult.Success {
		t.Fatalf("create under scope admission: %+v %v", created, err)
	}
	// 当前记录被外部删除时，读操作移除悬挂绑定，下一次输入创建新记录。
	if err := f.store.Sessions().Delete(ctx, created.Data.ID); err != nil {
		t.Fatal(err)
	}
	if current, err := f.s.Current(ctx, scope); err != nil || current.Status != operationresult.NoCurrentSession {
		t.Fatalf("missing current: %+v %v", current, err)
	}
	if f.s.CurrentBinding(scope) != nil {
		t.Fatal("missing row retained current")
	}
	if result, err := f.s.GetOrCreateCurrent(ctx, scope, "fresh"); err != nil || result.Status != operationresult.Success {
		t.Fatalf("get or create: %+v %v", result, err)
	}
}

func TestActivateModeCreatesAndReusesSession(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	if result, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{}); err != nil || result.Status != operationresult.InvalidArguments {
		t.Fatal("empty mode accepted without a current session")
	}
	if f.s.CurrentBinding(scope) != nil {
		t.Fatal("invalid mode created a binding")
	}
	created, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeChat, NewSessionTitle: "chat"})
	if err != nil || created.Status != operationresult.Success || created.Data.AlreadyActive || created.Data.Session.Mode != storage.SessionModeChat || created.Data.Session.Title != "chat" {
		t.Fatalf("mode creation: %+v %v", created, err)
	}
	pristine := f.s.CurrentBinding(scope)
	again, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeChat})
	if err != nil || again.Status != operationresult.Success || !again.Data.AlreadyActive || again.Data.Session.ID != created.Data.Session.ID || f.s.CurrentBinding(scope) != pristine {
		t.Fatalf("mode reuse: %+v %v", again, err)
	}
	for _, mode := range []string{"", "invalid"} {
		if result, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: mode}); err != nil || result.Status != operationresult.InvalidArguments {
			t.Fatalf("invalid mode %q accepted", mode)
		}
	}
	if err := f.store.Sessions().Delete(ctx, created.Data.Session.ID); err != nil {
		t.Fatal(err)
	}
	if result, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{}); err != nil || result.Status != operationresult.InvalidArguments {
		t.Fatal("empty mode accepted with a missing current row")
	}
	fresh, err := f.s.ActivateMode(ctx, scope, ActivateModeRequest{Mode: storage.SessionModeWork})
	if err != nil || fresh.Status != operationresult.Success || fresh.Data.Session.ID == created.Data.Session.ID || pristine.Valid() {
		t.Fatalf("missing current mode creation: %+v %v", fresh, err)
	}
}

func TestCreateResolvesAndValidatesMode(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	f.s.cfg.DefaultMode = storage.SessionModeChat
	result, err := f.s.Create(ctx, scope, CreateRequest{})
	if err != nil || result.Status != operationresult.Success || result.Data.Mode != storage.SessionModeChat {
		t.Fatalf("default mode: %+v %v", result, err)
	}
	result, err = f.s.Create(ctx, scope, CreateRequest{Mode: storage.SessionModeWork})
	if err != nil || result.Status != operationresult.Success || result.Data.Mode != storage.SessionModeWork {
		t.Fatalf("explicit mode: %+v %v", result, err)
	}
	original := f.s.CurrentBinding(scope)
	if result, err := f.s.Create(ctx, scope, CreateRequest{Mode: "invalid"}); err != nil || result.Status != operationresult.InvalidArguments {
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
	if _, err := f.s.Create(ctx, scope, CreateRequest{}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := f.s.Delete(ctx, scope, row.ID); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if f.s.CurrentBinding(scope) != b || !b.Valid() {
		t.Fatal("storage failure changed binding")
	}
	if sessions, bindings := f.releaseCounts(); sessions != 0 || bindings != 0 {
		t.Fatal("storage failure released state")
	}
}
