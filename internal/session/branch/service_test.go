package branch_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/session/binding"
	"elbot/internal/session/branch"
	"elbot/internal/session/control"
	"elbot/internal/session/share"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type fixture struct {
	s        *branch.Service
	c        *control.Service
	b        *binding.Manager
	store    *sqlite.Store
	deps     branch.ServiceDeps
	material func(context.Context, *storage.Session, *storage.Message) (*branch.PreparedMaterial, error)
	mu       sync.Mutex
	busy     []string
	released []string
	bindings []*share.Binding
}

type preparer struct{ f *fixture }

func (p preparer) PrepareFork(ctx context.Context, source *storage.Session, message *storage.Message) (*branch.PreparedMaterial, error) {
	return p.f.material(ctx, source, message)
}
func (p preparer) PrepareCopy(ctx context.Context, source *storage.Session) (*branch.PreparedMaterial, error) {
	return p.f.material(ctx, source, nil)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "branch.db"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{store: store, b: binding.NewManager()}
	f.material = func(context.Context, *storage.Session, *storage.Message) (*branch.PreparedMaterial, error) {
		return nil, nil
	}
	active := func() []string { f.mu.Lock(); defer f.mu.Unlock(); return append([]string(nil), f.busy...) }
	f.c = control.NewService(control.Config{DefaultMode: storage.SessionModeWork, IdleExpiration: control.IdleExpirationConfig{PrivateUserTTLMinutes: 1}}, control.ServiceDeps{
		Sessions: store.Sessions(), Messages: store.Messages(), Bindings: f.b, ActiveSessionIDs: active,
		CheckForeground: func(context.Context, *storage.Session) error { return nil }, IsSuperadmin: func(share.Scope) bool { return false },
		ReleaseSession: func(id string) { f.mu.Lock(); defer f.mu.Unlock(); f.released = append(f.released, id) },
		ReleaseBinding: func(b *share.Binding) { f.mu.Lock(); defer f.mu.Unlock(); f.bindings = append(f.bindings, b) },
	})
	f.deps = branch.ServiceDeps{Sessions: store.Sessions(), Messages: store.Messages(), Dialogues: store.Dialogues(),
		Bindings: f.c, ActiveSessionIDs: active, MaterialFor: func(llm.Origin) (branch.MaterialPreparer, error) { return preparer{f}, nil }}
	f.s = branch.NewService(f.deps)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := f.c.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := f.b.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func scope() share.Scope {
	return share.Scope{ActorID: "owner", Platform: "test", PlatformScopeID: "private:owner", ConversationKind: contextinfo.ConversationPrivate}
}

func (f *fixture) source(t *testing.T, mode string) *storage.Session {
	t.Helper()
	var row *storage.Session
	var err error
	if mode == storage.SessionModeBackground {
		row, err = f.c.PrepareBackground(t.Context(), scope(), control.BackgroundRequest{Kind: "cron", Name: "job"})
	} else {
		row, err = f.c.Create(t.Context(), scope(), control.CreateRequest{Mode: mode, Title: "source"})
	}
	if err != nil {
		t.Fatal(err)
	}
	row, err = f.store.Sessions().Mutate(t.Context(), row.ID, func(row *storage.Session) error {
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		if err := fields.Set("llm_origin", llm.Origin{APIType: "response", Provider: "p"}); err != nil {
			return err
		}
		if err := fields.Set("source_only", true); err != nil {
			return err
		}
		row.Metadata, err = fields.Encode()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func (f *fixture) message(t *testing.T, id, role string) *storage.Message {
	t.Helper()
	row := &storage.Message{SessionID: id, Role: role, Content: "message"}
	if err := f.store.Messages().Append(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	return row
}

func (f *fixture) setBusy(ids ...string) { f.mu.Lock(); defer f.mu.Unlock(); f.busy = ids }
func (f *fixture) releaseCounts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.released), len(f.bindings)
}
func (f *fixture) count(t *testing.T) int {
	t.Helper()
	rows, err := f.store.Sessions().List(context.Background(), storage.ListSessionsRequest{IncludeAllPlatforms: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func seed() *storage.NativeSeed {
	return &storage.NativeSeed{APIType: "response", Provider: "p", ItemsJSON: `[]`, MaterialsJSON: `[]`, ContinuationJSON: `[]`, CallsJSON: `[]`}
}

var errSave = errors.New("save failed")

type failingSessions struct{ storage.SessionRepository }

func (f failingSessions) Create(context.Context, *storage.Session) error { return errSave }
func (f failingSessions) CreateMaterial(context.Context, storage.SessionMaterialCreate) (events.LogResult, error) {
	return events.LogResult{}, errSave
}

func (f *fixture) failSave() {
	f.deps.Sessions = failingSessions{f.store.Sessions()}
	f.s = branch.NewService(f.deps)
}

func toolBatch(t *testing.T, f *fixture, sessionID, suffix string, pending bool) *storage.ToolBatch {
	t.Helper()
	status := "completed"
	if pending {
		status = "pending"
	}
	fields := storage.SessionMetadata{}
	if err := fields.Set("tool_batch_status", status); err != nil {
		t.Fatal(err)
	}
	if err := fields.Set("tool_calls", []map[string]string{{"ID": suffix + "-one", "Name": "tool", "Arguments": "{}"}, {"ID": suffix + "-two", "Name": "tool", "Arguments": "{}"}}); err != nil {
		t.Fatal(err)
	}
	if err := fields.Set(storage.ToolResultMessageKey, []string{suffix + "-r1", suffix + "-r2"}); err != nil {
		t.Fatal(err)
	}
	raw, err := fields.Encode()
	if err != nil {
		t.Fatal(err)
	}
	batch := &storage.ToolBatch{Call: &storage.Message{ID: suffix + "-head", SessionID: sessionID, Role: storage.RoleAssistant, Metadata: raw}, Results: []*storage.Message{
		{ID: suffix + "-r1", SessionID: sessionID, Role: storage.RoleTool, ToolCallID: suffix + "-one", Content: "one"},
		{ID: suffix + "-r2", SessionID: sessionID, Role: storage.RoleTool, ToolCallID: suffix + "-two", Content: "two"},
	}}
	if _, err := f.store.Dialogues().Commit(t.Context(), storage.DialogueCommit{SessionID: sessionID, ToolBatch: batch}); err != nil {
		t.Fatal(err)
	}
	return batch
}

type recordingSessions struct {
	storage.SessionRepository
	req storage.SessionMaterialCreate
}

func (r *recordingSessions) CreateMaterial(ctx context.Context, req storage.SessionMaterialCreate) (events.LogResult, error) {
	r.req = req
	return r.SessionRepository.CreateMaterial(ctx, req)
}

func TestNativeMaterialAtomicCommit(t *testing.T) {
	for _, operation := range []string{"fork", "copy", "compact"} {
		for _, fail := range []bool{false, true} {
			t.Run(operation+map[bool]string{false: "/commit", true: "/rollback"}[fail], func(t *testing.T) {
				f := newFixture(t)
				mode := storage.SessionModeWork
				if operation == "copy" {
					mode = storage.SessionModeBackground
				}
				source := f.source(t, mode)
				message := f.message(t, source.ID, storage.RoleAssistant)
				before := f.c.CurrentBinding(scope())
				mediaID := "media:" + strings.Repeat("b", 64)
				if err := f.store.Media().Upsert(t.Context(), &storage.Media{ID: mediaID, Backend: "local", MIMEType: "image/png"}); err != nil {
					t.Fatal(err)
				}
				material := seed()
				material.MediaIDs = []string{mediaID}
				if fail {
					material.ItemsJSON = "invalid"
				}
				f.material = func(context.Context, *storage.Session, *storage.Message) (*branch.PreparedMaterial, error) {
					return &branch.PreparedMaterial{Seed: material}, nil
				}
				repo := &recordingSessions{SessionRepository: f.store.Sessions()}
				f.deps.Sessions = repo
				f.s = branch.NewService(f.deps)
				var row *storage.Session
				var err error
				switch operation {
				case "fork":
					row, err = f.s.Fork(t.Context(), scope(), message.ID)
				case "copy":
					row, err = f.s.CopyBackground(t.Context(), scope(), branch.BackgroundCopyRequest{SourceSessionID: source.ID, Kind: "cron"})
				case "compact":
					row, err = f.s.CreateCompacted(t.Context(), scope(), source.ID, branch.CompactedRequest{ID: "reserved", Seed: material})
				}
				if (err != nil) != fail {
					t.Fatalf("result: %+v %v", row, err)
				}
				if repo.req.Session == nil {
					t.Fatal("did not use atomic material create")
				}
				persisted, getErr := f.store.Sessions().Get(t.Context(), repo.req.Session.ID)
				messages, listErr := f.store.Messages().ListBySession(t.Context(), repo.req.Session.ID)
				if listErr != nil {
					t.Fatal(listErr)
				}
				refs, refErr := f.store.MediaReferences().ListByOwner(t.Context(), "native_seed", material.ID)
				if refErr != nil {
					t.Fatal(refErr)
				}
				if fail {
					if !errors.Is(getErr, storage.ErrNotFound) || len(messages) != 0 || len(refs) != 0 || f.count(t) != 1 || f.c.CurrentBinding(scope()) != before {
						t.Fatalf("partial transaction: %+v %v messages=%d refs=%d", persisted, getErr, len(messages), len(refs))
					}
					return
				}
				if getErr != nil || len(refs) != 1 {
					t.Fatalf("missing commit: %+v %v refs=%d", persisted, getErr, len(refs))
				}
				records, err := f.store.Dialogues().LoadNativeWindow(t.Context(), persisted, nil)
				if err != nil || records.Seed == nil || records.Seed.ID != material.ID {
					t.Fatalf("native seed: %+v %v", records, err)
				}
				if operation == "copy" && len(messages) != 1 {
					t.Fatal("copy messages not saved")
				}
			})
		}
	}
}

func TestMaterialSelectionAndStaleContext(t *testing.T) {
	f := newFixture(t)
	source := f.source(t, storage.SessionModeWork)
	message := f.message(t, source.ID, storage.RoleAssistant)
	original := f.c.CurrentBinding(scope())
	if err := f.c.ResetCurrent(t.Context(), scope()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Resume(t.Context(), scope(), source.ID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.deps.MaterialFor = func(llm.Origin) (branch.MaterialPreparer, error) { calls++; return nil, errSave }
	f.s = branch.NewService(f.deps)
	if _, err := f.s.Fork(share.WithBinding(t.Context(), original), scope(), message.ID); err == nil || calls != 0 {
		t.Fatalf("stale binding reached preparation: %v calls=%d", err, calls)
	}
	if _, err := f.s.Fork(t.Context(), scope(), message.ID); !errors.Is(err, errSave) || calls != 1 {
		t.Fatalf("route failure lost: %v calls=%d", err, calls)
	}
	if f.count(t) != 1 {
		t.Fatal("route error fell back to display")
	}
	if _, err := f.store.Sessions().Mutate(t.Context(), source.ID, func(row *storage.Session) error { row.Metadata = `{}`; return nil }); err != nil {
		t.Fatal(err)
	}
	row, err := f.s.Fork(t.Context(), scope(), message.ID)
	if err != nil || calls != 1 || strings.Contains(row.Metadata, "llm_origin") {
		t.Fatalf("unknown origin inferred route: %+v %v calls=%d", row, err, calls)
	}
}
