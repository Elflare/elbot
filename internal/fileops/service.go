package fileops

import (
	"context"
	"fmt"
	"sync"

	"elbot/internal/contextinfo"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/workspace"
)

// Service shares path policy and memory backups between tools and
// slash commands. Session binding is supplied by Agent, not by model arguments.
type Service struct {
	Manager    *RollbackManager
	CheckWrite func(string) error
}

func NewService(checkWrite func(string) error) *Service {
	return &Service{Manager: NewRollbackManager(), CheckWrite: checkWrite}
}

type fileRollbackContextKey struct{}

type fileRollbackCall struct {
	service   *Service
	session   *RollbackSession
	mu        sync.Mutex
	path      string
	id        uint64
	admission CommitAdmission
	edit      *editPreflight
}

// WithBinding preserves the original lease and preview when a tool request
// context is derived after confirmation. A switched-away lease cannot revive.
func (s *Service) WithBinding(ctx context.Context, binding Binding, admission ...CommitAdmission) context.Context {
	if s == nil || s.Manager == nil {
		return ctx
	}
	if previous, ok := ctx.Value(fileRollbackContextKey{}).(*fileRollbackCall); ok && previous.service == s {
		return ctx
	}
	lease, _ := s.Manager.Session(binding)
	var enter CommitAdmission
	if len(admission) > 0 {
		enter = admission[0]
	}
	return context.WithValue(ctx, fileRollbackContextKey{}, &fileRollbackCall{service: s, session: lease, admission: enter})
}

// EditSession returns nil when recording is not enabled for this call (for
// example a background edit), but an invalid foreground lease is an error.
func (s *Service) EditSession(ctx context.Context) (*RollbackSession, error) {
	if sandboxctx.BackgroundContext(ctx) {
		return nil, nil
	}
	call, ok := ctx.Value(fileRollbackContextKey{}).(*fileRollbackCall)
	if !ok {
		return nil, nil
	}
	if call.service != s || call.session == nil {
		return nil, ErrRollbackExpired
	}
	return call.session, nil
}

func (s *Service) current(ctx context.Context) (*fileRollbackCall, error) {
	if sandboxctx.BackgroundContext(ctx) {
		return nil, fmt.Errorf("rollback is only available in foreground sessions")
	}
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || actor.Role != contextinfo.RoleSuperadmin {
		return nil, fmt.Errorf("rollback requires superadmin role")
	}
	if s == nil || s.Manager == nil {
		return nil, fmt.Errorf("file rollback is not configured")
	}
	call, ok := ctx.Value(fileRollbackContextKey{}).(*fileRollbackCall)
	if !ok || call.service != s || call.session == nil {
		return nil, ErrRollbackExpired
	}
	return call, nil
}

func (s *Service) List(ctx context.Context) ([]RollbackInfo, error) {
	call, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return call.session.List()
}

// RollbackByID resolves a human-facing backup number through the same lease and
// commit checks as tool rollback. On a selected record's failure, Path remains
// available for the caller's audit entry.
func (s *Service) RollbackByID(ctx context.Context, id uint64) (RollbackResult, error) {
	records, err := s.List(ctx)
	if err != nil {
		return RollbackResult{}, err
	}
	for _, record := range records {
		if record.ID == id {
			result, err := s.Rollback(ctx, record.Path, id)
			if err != nil {
				return RollbackResult{Path: record.Path}, err
			}
			return result, nil
		}
	}
	return RollbackResult{}, ErrRollbackNotFound
}

func (s *Service) Preview(ctx context.Context, rawPath string) (RollbackPreview, error) {
	call, err := s.current(ctx)
	if err != nil {
		return RollbackPreview{}, err
	}
	resolved, err := workspace.ResolveWorkspacePath(ctx, rawPath, workspace.PathResolveOptions{})
	if err != nil {
		return RollbackPreview{}, err
	}
	call.mu.Lock()
	defer call.mu.Unlock()
	if call.path != "" && call.path != resolved.Path {
		return RollbackPreview{}, fmt.Errorf("rollback path changed after preflight")
	}
	preview, err := call.session.Preview(ctx, resolved.Path, call.id, s.CheckWrite)
	if err != nil {
		return RollbackPreview{}, err
	}
	call.path, call.id = resolved.Path, preview.ID
	return preview, nil
}

// expectedID is only used by human-facing commands. Model calls use the record
// pinned by preflight, or the path's latest record when called directly.
func (s *Service) Rollback(ctx context.Context, rawPath string, expectedID uint64) (RollbackResult, error) {
	call, err := s.current(ctx)
	if err != nil {
		return RollbackResult{}, err
	}
	resolved, err := workspace.ResolveWorkspacePath(ctx, rawPath, workspace.PathResolveOptions{})
	if err != nil {
		return RollbackResult{}, err
	}
	call.mu.Lock()
	defer call.mu.Unlock()
	if call.path != "" && call.path != resolved.Path {
		return RollbackResult{}, fmt.Errorf("rollback path changed after preflight")
	}
	if call.id != 0 {
		if expectedID != 0 && call.id != expectedID {
			return RollbackResult{}, ErrRollbackNotFound
		}
		expectedID = call.id
	}
	ctx = withCommitValidation(ctx, func(locked context.Context) error {
		latest, err := workspace.ResolveWorkspacePath(locked, rawPath, workspace.PathResolveOptions{})
		if err != nil {
			return err
		}
		if latest.Path != resolved.Path {
			return fmt.Errorf("rollback path changed before commit")
		}
		return nil
	})
	return call.session.Rollback(ctx, resolved.Path, expectedID, s.CheckWrite)
}

// CommitAdmission is supplied by execution orchestration. File services never
// discover or replace the caller's Session binding.
type CommitAdmission func(context.Context) (context.Context, func(), error)

type commitValidationKey struct{}

func withCommitValidation(ctx context.Context, check func(context.Context) error) context.Context {
	return context.WithValue(ctx, commitValidationKey{}, check)
}
func enterCommit(ctx context.Context) (context.Context, func(), error) {
	locked, release := ctx, func() {}
	if call, ok := ctx.Value(fileRollbackContextKey{}).(*fileRollbackCall); ok && call.admission != nil {
		var err error
		locked, release, err = call.admission(ctx)
		if err != nil {
			return ctx, nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		release()
		return ctx, nil, err
	}
	if check, ok := ctx.Value(commitValidationKey{}).(func(context.Context) error); ok {
		if err := check(locked); err != nil {
			release()
			return ctx, nil, err
		}
	}
	return locked, release, nil
}
