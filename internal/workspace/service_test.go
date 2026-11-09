package workspace

import (
	"context"
	"os"
	"testing"
)

type serviceTestStore struct{ state State }

func (s *serviceTestStore) GetWorkspaceDir(context.Context) (string, error) { return s.state.Dir, nil }
func (s *serviceTestStore) SetWorkspaceDir(_ context.Context, dir string) error {
	s.state.SetAgentDir(dir)
	return nil
}
func (s *serviceTestStore) ClearWorkspaceDir(context.Context) error {
	s.state.Dir = ""
	return nil
}
func (s *serviceTestStore) ClearWorkspaceDirIfMatches(_ context.Context, dir string) (bool, error) {
	if s.state.Dir != dir {
		return false, nil
	}
	s.state.Dir = ""
	return true, nil
}
func (s *serviceTestStore) GetWorkspaceState(context.Context) (State, error) { return s.state, nil }
func (s *serviceTestStore) SetUserWorkspaceDir(_ context.Context, dir string) error {
	s.state.SetUserDir(dir)
	return nil
}
func (s *serviceTestStore) SetWorkspaceSync(_ context.Context, agentToUser, userToAgent bool) (State, error) {
	s.state.SetSync(agentToUser, userToAgent)
	return s.state, nil
}

func TestServiceReturnsDefaultWorkspaceWithoutPersistingIt(t *testing.T) {
	store := &serviceTestStore{}
	service := NewService(func(string) SessionStateStore { return store })

	state, err := service.GetSessionState(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if state.Dir != cwd || store.state.Dir != "" {
		t.Fatalf("effective=%q stored=%q; want cwd=%q and no persisted default", state.Dir, store.state.Dir, cwd)
	}
}

func TestServiceReturnsSessionWorkspacePath(t *testing.T) {
	dir := t.TempDir()
	store := &serviceTestStore{state: State{Dir: dir}}
	service := NewService(func(string) SessionStateStore { return store })

	state, err := service.GetSessionState(t.Context(), "session")
	if err != nil || state.Dir != dir {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
