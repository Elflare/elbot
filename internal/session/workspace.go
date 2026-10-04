package session

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"elbot/internal/storage"
	"elbot/internal/workspace"
)

// WorkspaceStore is a Session-scoped adapter, never a mutable row snapshot.
type WorkspaceStore struct {
	service    *Service
	repository storage.SessionRepository
	sessionID  string
}

func NewWorkspaceStore(service *Service, repository storage.SessionRepository, sessionID string) *WorkspaceStore {
	return &WorkspaceStore{service: service, repository: repository, sessionID: sessionID}
}

var _ workspace.WorkspaceAgentNoticeStore = (*WorkspaceStore)(nil)
var _ workspace.WorkspaceStore = (*WorkspaceStore)(nil)

func (s *WorkspaceStore) state(ctx context.Context) (workspace.State, error) {
	if err := ctx.Err(); err != nil {
		return workspace.State{}, err
	}
	if binding, ok := BindingFromContext(ctx); ok && (!binding.Valid() || binding.SessionID() != s.sessionID) {
		return workspace.State{}, fmt.Errorf("workspace session binding expired")
	}
	row, err := s.repository.Get(ctx, s.sessionID)
	if err != nil {
		return workspace.State{}, err
	}
	return workspace.DecodeState(row.Metadata)
}

func (s *WorkspaceStore) GetWorkspaceDir(ctx context.Context) (string, error) {
	state, err := s.state(ctx)
	return state.Dir, err
}
func (s *WorkspaceStore) SetWorkspaceDir(ctx context.Context, dir string) error {
	return s.SetWorkspaceDirWithAgentNotice(ctx, dir, false)
}
func (s *WorkspaceStore) ClearWorkspaceDir(ctx context.Context) error {
	return s.ClearWorkspaceDirWithAgentNotice(ctx, "", false)
}
func (s *WorkspaceStore) HasWorkspaceAgentNoticeDir(ctx context.Context, dir string) (bool, error) {
	state, err := s.state(ctx)
	return slices.Contains(state.AgentNoticeDirs, strings.TrimSpace(dir)), err
}
func (s *WorkspaceStore) MarkWorkspaceAgentNoticeDir(ctx context.Context, dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	return s.save(ctx, func(state *workspace.State) { markWorkspaceNotice(state, dir) })
}
func (s *WorkspaceStore) SetWorkspaceDirWithAgentNotice(ctx context.Context, dir string, mark bool) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	return s.save(ctx, func(state *workspace.State) {
		state.Dir = dir
		if mark {
			markWorkspaceNotice(state, dir)
		}
	})
}
func (s *WorkspaceStore) ClearWorkspaceDirWithAgentNotice(ctx context.Context, dir string, mark bool) error {
	return s.save(ctx, func(state *workspace.State) {
		state.Dir = ""
		if mark {
			markWorkspaceNotice(state, strings.TrimSpace(dir))
		}
	})
}

// EnsureWorkspaceDir initializes background workspace without overwriting a
// foreground selection made concurrently with adoption.
func (s *WorkspaceStore) EnsureWorkspaceDir(ctx context.Context, dir string) error {
	return s.save(ctx, func(state *workspace.State) {
		if state.Dir == "" {
			state.Dir = strings.TrimSpace(dir)
		}
	})
}
func markWorkspaceNotice(state *workspace.State, dir string) {
	if dir != "" && !slices.Contains(state.AgentNoticeDirs, dir) {
		state.AgentNoticeDirs = append(state.AgentNoticeDirs, dir)
	}
}
func (s *WorkspaceStore) save(ctx context.Context, update func(*workspace.State)) error {
	if s.service != nil {
		var release func()
		var err error
		if binding, ok := BindingFromContext(ctx); ok {
			if binding == nil || binding.SessionID() != s.sessionID {
				return fmt.Errorf("workspace session binding mismatch")
			}
			ctx, release, err = s.service.EnterBinding(ctx, binding)
		} else {
			ctx, release, err = s.service.EnterSessions(ctx, s.sessionID)
		}
		if err != nil {
			return err
		}
		defer release()
	}
	_, err := s.repository.Mutate(ctx, s.sessionID, func(row *storage.Session) error {
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		state, err := workspace.DecodeState(row.Metadata)
		if err != nil {
			return err
		}
		update(&state)
		slices.Sort(state.AgentNoticeDirs)
		state.AgentNoticeDirs = slices.Compact(state.AgentNoticeDirs)
		if state.Dir == "" {
			delete(fields, "workspace_dir")
		} else if err := fields.Set("workspace_dir", state.Dir); err != nil {
			return err
		}
		if len(state.AgentNoticeDirs) == 0 {
			delete(fields, "workspace_agent_notice_dirs")
		} else if err := fields.Set("workspace_agent_notice_dirs", state.AgentNoticeDirs); err != nil {
			return err
		}
		encoded, err := fields.Encode()
		if err != nil {
			return err
		}
		if row.Metadata != encoded {
			row.Metadata = encoded
			row.UpdatedAt = storage.Now()
		}
		return nil
	})
	return err
}
