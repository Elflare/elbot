package fileops

import (
	"context"
	"fmt"

	"elbot/internal/contextinfo"
)

type fileBindingContextKey struct{}

type fileBinding struct {
	// service identifies the owner of this execution environment.
	service *Service
	// session retains the original lease; expiration cannot be reversed.
	session *RollbackSession
	// admission enters session coordination only during commit.
	admission CommitAdmission
}

// WithBinding preserves the original environment in derived contexts.
// Prepared operations are explicit values, never context values.
func (s *Service) WithBinding(ctx context.Context, binding Binding, admission ...CommitAdmission) context.Context {
	if previous, ok := ctx.Value(fileBindingContextKey{}).(*fileBinding); ok && previous.service == s {
		return ctx
	}
	lease, _ := s.Manager.Session(binding)
	var enter CommitAdmission
	if len(admission) > 0 {
		enter = admission[0]
	}
	return context.WithValue(ctx, fileBindingContextKey{}, &fileBinding{service: s, session: lease, admission: enter})
}

// EditSession returns the captured rollback lease, or nil for unrecorded edits.
// Writes validate the lease and enter the caller's commit admission before committing.
func (s *Service) EditSession(ctx context.Context) (*RollbackSession, error) {
	binding, ok := ctx.Value(fileBindingContextKey{}).(*fileBinding)
	if !ok {
		return nil, nil
	}
	if binding.service != s {
		return nil, ErrRollbackExpired
	}
	return binding.session, nil
}

func (s *Service) editBinding(ctx context.Context) (*fileBinding, *RollbackSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	binding, _ := ctx.Value(fileBindingContextKey{}).(*fileBinding)
	lease, err := s.EditSession(ctx)
	if err != nil {
		return nil, nil, err
	}
	if lease != nil {
		if err := lease.check(ctx); err != nil {
			return nil, nil, err
		}
	}
	return binding, lease, nil
}

func (s *Service) rollbackBinding(ctx context.Context) (*fileBinding, error) {
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || actor.Role != contextinfo.RoleSuperadmin {
		return nil, fmt.Errorf("rollback requires superadmin role")
	}
	binding, ok := ctx.Value(fileBindingContextKey{}).(*fileBinding)
	if !ok || binding.service != s || binding.session == nil {
		return nil, ErrRollbackExpired
	}
	return binding, nil
}
