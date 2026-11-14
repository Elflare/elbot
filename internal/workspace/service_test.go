package workspace

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"elbot/internal/operationresult"
)

type serviceTestStore struct {
	state       State
	beforeClear func()
}

func (s *serviceTestStore) GetWorkspaceDir(context.Context) (string, error) { return s.state.Dir, nil }
func (s *serviceTestStore) SetWorkspaceDir(_ context.Context, dir string) error {
	s.state.SetAgentDir(dir)
	return nil
}
func (s *serviceTestStore) ClearWorkspaceDir(context.Context) error {
	s.state.ClearAgentDir("", false)
	return nil
}
func (s *serviceTestStore) ClearWorkspaceDirIfMatches(_ context.Context, dir string) (bool, error) {
	if s.beforeClear != nil {
		s.beforeClear()
		s.beforeClear = nil
	}
	return s.state.ClearAgentDirIfMatches(dir), nil
}
func (s *serviceTestStore) GetWorkspaceState(context.Context) (State, error) { return s.state, nil }
func (s *serviceTestStore) SetUserWorkspaceDir(_ context.Context, dir string) error {
	s.state.SetUserDir(dir)
	return nil
}
func (s *serviceTestStore) SetWorkspaceSync(_ context.Context, agentToUser, userToAgent bool, agentDir string) (State, error) {
	s.state.SetSync(agentToUser, userToAgent, agentDir)
	return s.state, nil
}
func workspaceServiceForTest(store *serviceTestStore) *Service {
	return NewService(func(context.Context) (operationresult.Result[string], error) {
		return operationresult.Result[string]{Status: operationresult.Success, Data: "session"}, nil
	}, func(string) SessionStateStore { return store })
}

func TestServiceRefreshesStateAfterDirectoryCleanup(t *testing.T) {
	for _, scenario := range []string{"default", "existing", "missing", "concurrent_selection"} {
		t.Run(scenario, func(t *testing.T) {
			store := &serviceTestStore{state: State{AgentToUser: true}}
			switch scenario {
			case "existing":
				store.state.SetAgentDir(t.TempDir())
			case "missing", "concurrent_selection":
				store.state.SetAgentDir(filepath.Join(t.TempDir(), "missing"))
				store.state.AgentNoticeDirs = []string{store.state.Dir}
			}
			if scenario == "concurrent_selection" {
				selected := t.TempDir()
				store.beforeClear = func() {
					store.state.SetAgentDir(selected)
					store.state.UserToAgent = true
				}
			}
			result, err := workspaceServiceForTest(store).GetCurrentState(t.Context())
			if err != nil || result.Status != operationresult.Success {
				t.Fatalf("workspace: %+v %v", result, err)
			}
			want := store.state
			if scenario == "default" || scenario == "missing" {
				if store.state.Dir != "" {
					t.Fatalf("default directory persisted: %+v", store.state)
				}
				want.Dir, err = os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
			}
			if result.Data.SessionID != "session" || !reflect.DeepEqual(result.Data.State, want) {
				t.Fatalf("returned=%+v want=%+v", result.Data, want)
			}
		})
	}
}

func TestServiceSyncReturnsCurrentState(t *testing.T) {
	for _, scenario := range []string{"default", "missing_user"} {
		t.Run(scenario, func(t *testing.T) {
			store := &serviceTestStore{}
			if scenario == "missing_user" {
				store.state = State{Dir: t.TempDir(), UserDir: filepath.Join(t.TempDir(), "missing"), AgentToUser: true}
			}
			result, err := workspaceServiceForTest(store).SetSync(t.Context(), true, scenario == "missing_user")
			if err != nil || result.Status != operationresult.Success {
				t.Fatalf("sync: %+v %v", result, err)
			}
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			want := store.state
			want.Dir = cwd
			if store.state.Dir != "" || !reflect.DeepEqual(result.Data.State, want) {
				t.Fatalf("returned=%+v stored=%+v want=%+v", result.Data.State, store.state, want)
			}
			if scenario == "default" && want.UserDir != cwd {
				t.Fatalf("default user directory=%q want=%q", want.UserDir, cwd)
			}
			if scenario == "missing_user" && want.UserDir != "" {
				t.Fatalf("missing user directory retained: %+v", want)
			}
		})
	}
}
