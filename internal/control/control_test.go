package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func testContext(admin bool) context.Context {
	role := contextinfo.RoleUser
	if admin {
		role = contextinfo.RoleSuperadmin
	}
	return contextinfo.WithActor(context.Background(), contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: "cli", ID: "test"}, Role: role})
}

func testSupervisor(t *testing.T, deps SupervisorDeps) (*Supervisor, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{ConfigPath: filepath.Join(dir, "custom.toml")}
	if err := os.WriteFile(cfg.ConfigPath, []byte("initial\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	m, err := NewSupervisor(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	return m, dir
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("error = %#v; want code %s", err, code)
	}
}

func TestMissingCapabilitiesAndIdentity(t *testing.T) {
	m, _ := testSupervisor(t, SupervisorDeps{})
	_, err := m.ListCommands(testContext(true), ListCommandsRequest{})
	requireCode(t, err, "not_configured")
	_, err = m.Status(testContext(true))
	requireCode(t, err, "not_configured")
	_, err = m.ListHooks(context.Background())
	requireCode(t, err, "permission_denied")
	_, err = m.ListFiles(testContext(false), ListFilesRequest{})
	requireCode(t, err, "permission_denied")
	_, err = m.WriteFile(testContext(false), WriteFileRequest{Root: "skills", Path: "new/SKILL.md", Content: "x", Create: true})
	requireCode(t, err, "permission_denied")
	ctx, cancel := context.WithCancel(testContext(true))
	cancel()
	_, err = m.ListFiles(ctx, ListFilesRequest{})
	requireCode(t, err, "canceled")
	if ClassifyError(nil) != nil {
		t.Fatal("nil error became a non-nil interface")
	}
}

func TestWorkspaceDirectoryBrowsingRequiresIdentityEvenWithExplicitPath(t *testing.T) {
	m, _ := testSupervisor(t, SupervisorDeps{})
	_, err := m.WorkspaceDirectories(context.Background(), WorkspaceDirectoriesRequest{Path: t.TempDir()})
	requireCode(t, err, "permission_denied")
}

func TestCommandsFilterWithoutMutatingCatalog(t *testing.T) {
	items := []CommandInfo{{Name: "new", Aliases: []string{"n"}, MinRole: contextinfo.RoleUser}, {Name: "doctor"}}
	m, _ := testSupervisor(t, SupervisorDeps{Commands: func(context.Context) ([]CommandInfo, error) { return items, nil }})
	listed, err := m.ListCommands(testContext(false), ListCommandsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("items: %+v", listed.Items)
	}
	listed.Items[0].Aliases[0] = "changed"
	if items[0].Aliases[0] != "n" {
		t.Fatal("catalog aliases were mutated")
	}
	_, err = m.ListCommands(testContext(false), ListCommandsRequest{Name: "doctor"})
	requireCode(t, err, "not_found")
	listed, err = m.ListCommands(testContext(true), ListCommandsRequest{Name: "n"})
	if err != nil || len(listed.Items) != 1 {
		t.Fatalf("alias lookup: %+v %v", listed, err)
	}
}

type sessionStub struct {
	SessionCapabilities
	current       *storage.Session
	currentErr    error
	resets        int
	deletes       int
	modeErr       error
	resetErr      error
	deleteErr     error
	modeRequest   session.ActivateModeRequest
	cleanupBefore time.Time
}

func (s *sessionStub) Current(context.Context, session.Scope) (*storage.Session, error) {
	return s.current, s.currentErr
}
func (s *sessionStub) CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error) {
	return s.current, nil, s.currentErr
}
func (s *sessionStub) DefaultMode() string { return "work" }
func (s *sessionStub) ResetCurrent(context.Context, session.Scope) error {
	s.resets++
	if s.resetErr != nil {
		return s.resetErr
	}
	s.current, s.currentErr = nil, session.ErrNoCurrentSession
	return nil
}
func (s *sessionStub) ActivateMode(_ context.Context, _ session.Scope, req session.ActivateModeRequest) (session.ActivateModeResult, error) {
	s.modeRequest = req
	if s.modeErr != nil {
		return session.ActivateModeResult{}, s.modeErr
	}
	return session.ActivateModeResult{Session: &storage.Session{ID: "mode", Mode: req.Mode}, AlreadyActive: true}, nil
}
func (s *sessionStub) Delete(context.Context, session.Scope, string) error {
	s.deletes++
	return s.deleteErr
}
func (s *sessionStub) CleanupExpired(_ context.Context, before time.Time) (int, error) {
	s.cleanupBefore = before
	return 2, errors.New("partial cleanup")
}
func (s *sessionStub) EnterSessions(ctx context.Context, _ ...string) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

type executionStub struct {
	ExecutionCapabilities
	stopped []string
}

func (s *executionStub) StopSession(id string)        { s.stopped = append(s.stopped, id) }
func (s *executionStub) SnapshotAll() []turn.Snapshot { return nil }

func TestSessionAdmissionAndContinuation(t *testing.T) {
	sessions := &sessionStub{current: &storage.Session{ID: "old", Mode: "work"}}
	m, _ := testSupervisor(t, SupervisorDeps{Sessions: sessions, Scope: func(context.Context) session.Scope { return session.Scope{} }})
	sessions.resetErr = session.ErrSessionBusy
	_, err := m.NewSession(testContext(false))
	requireCode(t, err, "session_busy")
	if !errors.Is(err, session.ErrSessionBusy) || sessions.resets != 1 {
		t.Fatal("session provider did not decide creation admission")
	}
	sessions.deleteErr = session.ErrSessionBusy
	_, err = m.DeleteSession(testContext(false), DeleteSessionRequest{SessionID: "old", Confirmed: true})
	requireCode(t, err, "session_busy")
	if !errors.Is(err, session.ErrSessionBusy) || sessions.deletes != 1 {
		t.Fatal("session provider did not decide deletion admission")
	}
	_, err = m.DeleteSession(testContext(false), DeleteSessionRequest{SessionID: "old"})
	requireCode(t, err, "confirmation_required")
	if sessions.deletes != 1 {
		t.Fatal("unconfirmed deletion reached provider")
	}
	text := "  exact input\n"
	result, err := m.SetMode(testContext(false), SetModeRequest{Mode: "chat", Text: text, NewSessionTitle: "chosen title"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Continuation == nil || result.Continuation.Text != text || result.Continuation.SessionID != "mode" {
		t.Fatalf("continuation = %+v", result.Continuation)
	}
	if sessions.modeRequest.NewSessionTitle != "chosen title" {
		t.Fatal("caller-selected title was not forwarded")
	}
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "exact input") {
		t.Fatal("internal continuation leaked onto wire")
	}
	sessions.modeErr = session.ErrChatModeRequiresEmptySession
	failed, err := m.SetMode(testContext(false), SetModeRequest{Mode: "chat", Text: "never send"})
	requireCode(t, err, "mode_conflict")
	if failed.Continuation != nil {
		t.Fatal("failed switch returned a continuation")
	}
	_, err = m.CleanSessions(testContext(false), CleanSessionsRequest{Confirmed: true})
	requireCode(t, err, "permission_denied")
	before := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	partial, err := m.CleanSessions(testContext(true), CleanSessionsRequest{Confirmed: true, Before: before})
	if err == nil || partial.AffectedCount != 2 {
		t.Fatalf("lost partial result: %+v %v", partial, err)
	}
	if !sessions.cleanupBefore.Equal(before) {
		t.Fatal("caller-selected cleanup cutoff was not forwarded")
	}
}

type restartStub struct {
	mu       sync.Mutex
	reserved bool
	commits  int
	cancels  int
}

func (s *restartStub) PrepareRestart(context.Context) (RestartPreparation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reserved {
		return RestartPreparation{}, &Error{Code: "restart_pending", Message: "重启已预留"}
	}
	s.reserved = true
	return RestartPreparation{Commit: func(context.Context) error { s.mu.Lock(); defer s.mu.Unlock(); s.commits++; return nil }, Cancel: func() { s.mu.Lock(); defer s.mu.Unlock(); s.cancels++; s.reserved = false }}, nil
}

func TestRestartDeliveryOwnsCommit(t *testing.T) {
	provider := &restartStub{}
	m, _ := testSupervisor(t, SupervisorDeps{Runtime: provider})
	_, err := m.RequestRestart(testContext(false))
	requireCode(t, err, "permission_denied")
	result, err := m.RequestRestart(testContext(true))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted || provider.commits != 0 {
		t.Fatal("prepare restarted before delivery")
	}
	_, err = m.RequestRestart(testContext(true))
	requireCode(t, err, "restart_pending")
	result.Cancel()
	result.Cancel()
	requireCode(t, result.Commit(testContext(true)), "canceled")
	if provider.commits != 0 || provider.cancels != 1 {
		t.Fatalf("cancel counts: %+v", provider)
	}
	result, err = m.RequestRestart(testContext(true))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if err := result.Commit(testContext(true)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	result.Cancel()
	if provider.commits != 1 || provider.cancels != 1 {
		t.Fatalf("commit counts: %+v", provider)
	}
	wire, err := json.Marshal(result)
	if err != nil || string(wire) != "{\"accepted\":true}" {
		t.Fatalf("restart wire: %s %v", wire, err)
	}
}
