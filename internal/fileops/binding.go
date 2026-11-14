package fileops

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
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
	lease, _ := s.manager.Session(binding)
	var enter CommitAdmission
	if len(admission) > 0 {
		enter = admission[0]
	}
	return context.WithValue(ctx, fileBindingContextKey{}, &fileBinding{service: s, session: lease, admission: enter})
}

// EditSession returns the captured rollback lease, or nil for unrecorded edits.
// Writes validate the lease and enter the caller's commit admission before committing.
func (s *Service) EditSession(ctx context.Context) operationresult.Result[*RollbackSession] {
	binding, ok := ctx.Value(fileBindingContextKey{}).(*fileBinding)
	if !ok {
		return operationresult.Result[*RollbackSession]{Status: operationresult.Success}
	}
	if binding.service != s {
		return operationresult.Result[*RollbackSession]{Status: operationresult.Conflict, Message: "rollback session expired"}
	}
	return operationresult.Result[*RollbackSession]{Status: operationresult.Success, Data: binding.session}
}

func (s *Service) editBinding(ctx context.Context) (operationresult.Result[*fileBinding], *RollbackSession, error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*fileBinding]{}, nil, err
	}
	binding, _ := ctx.Value(fileBindingContextKey{}).(*fileBinding)
	lease := s.EditSession(ctx)
	if lease.Status != operationresult.Success {
		return operationresult.Result[*fileBinding]{Status: lease.Status, Message: lease.Message}, nil, nil
	}
	if lease.Data != nil {
		checked, err := lease.Data.check(ctx)
		if err != nil {
			return operationresult.Result[*fileBinding]{}, nil, err
		}
		if checked.Status != operationresult.Success {
			return operationresult.Result[*fileBinding]{Status: checked.Status, Message: checked.Message}, nil, nil
		}
	}
	return operationresult.Result[*fileBinding]{Status: operationresult.Success, Data: binding}, lease.Data, nil
}

func (s *Service) rollbackBinding(ctx context.Context) operationresult.Result[*fileBinding] {
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || actor.Role != contextinfo.RoleSuperadmin {
		return operationresult.Result[*fileBinding]{Status: operationresult.Forbidden, Message: "rollback requires superadmin role"}
	}
	binding, ok := ctx.Value(fileBindingContextKey{}).(*fileBinding)
	if !ok || binding.service != s || binding.session == nil {
		return operationresult.Result[*fileBinding]{Status: operationresult.Conflict, Message: "rollback session expired"}
	}
	return operationresult.Result[*fileBinding]{Status: operationresult.Success, Data: binding}
}
