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
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
	"elbot/internal/workspace"
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

func requireStatus[T any](t *testing.T, result operationresult.Result[T], status operationresult.Status) {
	t.Helper()
	if result.Status != status {
		t.Fatalf("status = %q; want %q", result.Status, status)
	}
}

func TestIdentityAndCancellation(t *testing.T) {
	m, _ := testSupervisor(t, SupervisorDeps{})
	result, err := m.ListHooks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result, operationresult.Forbidden)
	listedFiles, err := m.ListFiles(testContext(false), ListFilesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, listedFiles, operationresult.Forbidden)
	resultWrite, err := m.WriteFile(testContext(false), WriteFileRequest{Root: "skills", Path: "new/SKILL.md", Content: "x", Create: true})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, resultWrite, operationresult.Forbidden)
	ctx, cancel := context.WithCancel(testContext(true))
	cancel()
	_, err = m.ListFiles(ctx, ListFilesRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request error = %v", err)
	}
}

func TestWorkspaceFilesUsesRequestedSideAndRelativePath(t *testing.T) {
	agentRoot, userRoot := t.TempDir(), t.TempDir()
	for root, name := range map[string]string{agentRoot: "agent.txt", userRoot: "user.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sessions := &sessionStub{
		current: &storage.Session{ID: "current"},
	}
	m, _ := testSupervisor(t, SupervisorDeps{
		Sessions: sessions,
		Scope:    func(context.Context) session.Scope { return session.Scope{} },
	})
	m.workspaces = workspaceCapabilitiesStub{state: workspace.State{Dir: agentRoot, UserDir: userRoot}}
	for side, want := range map[string]string{"agent": "agent.txt", "user": "user.txt"} {
		result, err := m.WorkspaceFiles(testContext(false), WorkspaceFilesRequest{Side: side})
		if err != nil || result.Status != operationresult.Success || result.Data.Side != side || result.Data.Path != "." || len(result.Data.Items) != 1 || result.Data.Items[0].Name != want {
			t.Fatalf("side=%s result=%+v err=%v", side, result, err)
		}
	}
	invalid, err := m.WorkspaceFiles(testContext(false), WorkspaceFilesRequest{Side: "invalid"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, invalid, operationresult.InvalidArguments)
	traversal, err := m.WorkspaceFiles(testContext(false), WorkspaceFilesRequest{Side: "agent", Path: "../outside"})
	if err != nil || traversal.Status != operationresult.InvalidArguments {
		t.Fatal("workspace traversal accepted")
	}
}

type workspaceCapabilitiesStub struct{ state workspace.State }

func (s workspaceCapabilitiesStub) GetCurrentState(context.Context) (operationresult.Result[workspace.CurrentState], error) {
	return operationresult.Result[workspace.CurrentState]{Status: operationresult.Success, Data: workspace.CurrentState{SessionID: "current", State: s.state}}, nil
}
func (s workspaceCapabilitiesStub) SetUserDir(_ context.Context, dir string) (operationresult.Result[workspace.CurrentState], error) {
	s.state.SetUserDir(dir)
	return operationresult.Result[workspace.CurrentState]{Status: operationresult.Success, Data: workspace.CurrentState{SessionID: "current", State: s.state}}, nil
}
func (s workspaceCapabilitiesStub) SetSync(_ context.Context, agentToUser, userToAgent bool) (operationresult.Result[workspace.CurrentState], error) {
	s.state.SetSync(agentToUser, userToAgent, s.state.Dir)
	return operationresult.Result[workspace.CurrentState]{Status: operationresult.Success, Data: workspace.CurrentState{SessionID: "current", State: s.state}}, nil
}

func TestWorkspaceDirectoryBrowsingRequiresIdentityEvenWithExplicitPath(t *testing.T) {
	m, _ := testSupervisor(t, SupervisorDeps{})
	result, err := m.WorkspaceDirectories(context.Background(), WorkspaceDirectoriesRequest{Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result, operationresult.Forbidden)
}

func TestCommandsFilterWithoutMutatingCatalog(t *testing.T) {
	items := []CommandInfo{{Name: "new", Aliases: []string{"n"}, MinRole: contextinfo.RoleUser}, {Name: "doctor"}}
	m, _ := testSupervisor(t, SupervisorDeps{Commands: func(context.Context) ([]CommandInfo, error) { return items, nil }})
	listed, err := m.ListCommands(testContext(false), ListCommandsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if listed.Status != operationresult.Success || len(listed.Data.Items) != 1 {
		t.Fatalf("items: %+v", listed)
	}
	listed.Data.Items[0].Aliases[0] = "changed"
	if items[0].Aliases[0] != "n" {
		t.Fatal("catalog aliases were mutated")
	}
	missing, err := m.ListCommands(testContext(false), ListCommandsRequest{Name: "doctor"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, missing, operationresult.NotFound)
	listed, err = m.ListCommands(testContext(true), ListCommandsRequest{Name: "n"})
	if err != nil || listed.Status != operationresult.Success || len(listed.Data.Items) != 1 {
		t.Fatalf("alias lookup: %+v %v", listed, err)
	}
}

type sessionStub struct {
	SessionCapabilities
	current       *storage.Session
	currentStatus operationresult.Status
	resets        int
	deletes       int
	resetStatus   operationresult.Status
	deleteStatus  operationresult.Status
	modeStatus    operationresult.Status
	modeRequest   session.ActivateModeRequest
	cleanupBefore time.Time
}

func (s *sessionStub) currentResult() (operationresult.Result[*storage.Session], error) {
	if s.currentStatus != "" && s.currentStatus != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: s.currentStatus}, nil
	}
	return operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: s.current}, nil
}
func (s *sessionStub) Current(context.Context, session.Scope) (operationresult.Result[*storage.Session], error) {
	return s.currentResult()
}
func (s *sessionStub) CurrentBound(context.Context, session.Scope) (operationresult.Result[session.CurrentBoundData], error) {
	current, err := s.currentResult()
	if err != nil || current.Status != operationresult.Success {
		return operationresult.Result[session.CurrentBoundData]{Status: current.Status, Message: current.Message}, err
	}
	return operationresult.Result[session.CurrentBoundData]{Status: operationresult.Success, Data: session.CurrentBoundData{Session: current.Data}}, nil
}
func (s *sessionStub) DefaultMode() string { return "work" }
func (s *sessionStub) ResetCurrent(context.Context, session.Scope) (operationresult.Result[struct{}], error) {
	s.resets++
	if s.resetStatus != "" && s.resetStatus != operationresult.Success {
		return operationresult.Result[struct{}]{Status: s.resetStatus, Message: "session operation rejected"}, nil
	}
	s.current, s.currentStatus = nil, operationresult.NoCurrentSession
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}
func (s *sessionStub) ActivateMode(_ context.Context, _ session.Scope, req session.ActivateModeRequest) (operationresult.Result[session.ActivateModeResult], error) {
	s.modeRequest = req
	if s.modeStatus != "" && s.modeStatus != operationresult.Success {
		return operationresult.Result[session.ActivateModeResult]{Status: s.modeStatus, Message: "mode rejected"}, nil
	}
	return operationresult.Result[session.ActivateModeResult]{Status: operationresult.Success, Data: session.ActivateModeResult{Session: &storage.Session{ID: "mode", Mode: req.Mode}, AlreadyActive: true}}, nil
}
func (s *sessionStub) Delete(context.Context, session.Scope, string) (operationresult.Result[struct{}], error) {
	s.deletes++
	if s.deleteStatus != "" && s.deleteStatus != operationresult.Success {
		return operationresult.Result[struct{}]{Status: s.deleteStatus, Message: "delete rejected"}, nil
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
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
	sessions.resetStatus = operationresult.Busy
	reset, err := m.NewSession(testContext(false))
	requireStatus(t, reset, operationresult.Busy)
	if err != nil || sessions.resets != 1 {
		t.Fatal("session provider did not decide creation admission")
	}
	sessions.deleteStatus = operationresult.Busy
	deleted, err := m.DeleteSession(testContext(false), DeleteSessionRequest{SessionID: "old", Confirmed: true})
	requireStatus(t, deleted, operationresult.Busy)
	if err != nil || sessions.deletes != 1 {
		t.Fatal("session provider did not decide deletion admission")
	}
	unconfirmed, err := m.DeleteSession(testContext(false), DeleteSessionRequest{SessionID: "old"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, unconfirmed, operationresult.Rejected)
	if sessions.deletes != 1 {
		t.Fatal("unconfirmed deletion reached provider")
	}
	text := "  exact input\n"
	result, err := m.SetMode(testContext(false), SetModeRequest{Mode: "chat", Text: text, NewSessionTitle: "chosen title"})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if result.Data.Continuation == nil || result.Data.Continuation.Text != text || result.Data.Continuation.SessionID != "mode" {
		t.Fatalf("continuation = %+v", result.Data.Continuation)
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
	sessions.modeStatus = operationresult.Conflict
	failed, err := m.SetMode(testContext(false), SetModeRequest{Mode: "chat", Text: "never send"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, failed, operationresult.Conflict)
	if failed.Data.Continuation != nil {
		t.Fatal("failed switch returned a continuation")
	}
	unauthorized, err := m.CleanSessions(testContext(false), CleanSessionsRequest{Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, unauthorized, operationresult.Forbidden)
	before := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	_, err = m.CleanSessions(testContext(true), CleanSessionsRequest{Confirmed: true, Before: before})
	if err == nil || !strings.Contains(err.Error(), "partial cleanup") {
		t.Fatalf("cleanup error = %v", err)
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

func (s *restartStub) PrepareRestart(context.Context) (operationresult.Result[RestartPreparation], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reserved {
		return operationresult.Result[RestartPreparation]{Status: operationresult.Busy, Message: "重启已预留"}, nil
	}
	s.reserved = true
	return operationresult.Result[RestartPreparation]{Status: operationresult.Success, Data: RestartPreparation{Commit: func(context.Context) error { s.mu.Lock(); defer s.mu.Unlock(); s.commits++; return nil }, Cancel: func() { s.mu.Lock(); defer s.mu.Unlock(); s.cancels++; s.reserved = false }}}, nil
}

func TestRestartDeliveryOwnsCommit(t *testing.T) {
	provider := &restartStub{}
	m, _ := testSupervisor(t, SupervisorDeps{Runtime: provider})
	forbidden, err := m.RequestRestart(testContext(false))
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, forbidden, operationresult.Forbidden)
	result, err := m.RequestRestart(testContext(true))
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	data := result.Data
	if !data.Accepted || provider.commits != 0 {
		t.Fatal("prepare restarted before delivery")
	}
	busy, err := m.RequestRestart(testContext(true))
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, busy, operationresult.Busy)
	data.Cancel()
	data.Cancel()
	if err := data.Commit(testContext(true)); err != nil {
		t.Fatalf("commit after cancellation: %v", err)
	}
	if provider.commits != 0 || provider.cancels != 1 {
		t.Fatalf("cancel counts: %+v", provider)
	}
	result, err = m.RequestRestart(testContext(true))
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	data = result.Data
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if err := data.Commit(testContext(true)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	data.Cancel()
	if provider.commits != 1 || provider.cancels != 1 {
		t.Fatalf("commit counts: %+v", provider)
	}
	wire, err := json.Marshal(data)
	if err != nil || string(wire) != "{\"accepted\":true}" {
		t.Fatalf("restart wire: %s %v", wire, err)
	}
}
