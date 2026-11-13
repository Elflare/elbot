package input

import (
	"context"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

type FileCommandPreparer struct {
	service interface {
		WithBinding(context.Context, fileops.Binding, ...fileops.CommitAdmission) context.Context
	}
	sessions interface {
		CurrentBound(context.Context, session.Scope) (operationresult.Result[session.CurrentBoundData], error)
		EnterBinding(context.Context, *session.Binding) (context.Context, func(), error)
		EnterSessions(context.Context, ...string) (context.Context, func(), error)
	}
	NewWorkspaceStore func(string) workspace.WorkspaceStore
	turns             interface{ Snapshot(string) turn.Snapshot }
	requests          interface {
		ListBySession(string) []request.Request
	}
	identity interface {
		Scope(context.Context) session.Scope
		Actor(context.Context) contextinfo.Actor
	}
}

// PrepareFileCommand captures the original binding and execution admission for
// file commands. The file service owns record lookup and the actual operation.
func (p *FileCommandPreparer) PrepareFileCommand(ctx context.Context, idleOnly bool) (operationresult.Result[context.Context], error) {
	if p.identity.Actor(ctx).Role != contextinfo.RoleSuperadmin {
		return operationresult.Result[context.Context]{Status: operationresult.Forbidden, Message: "rollback requires superadmin role"}, nil
	}
	current, err := p.sessions.CurrentBound(ctx, p.identity.Scope(ctx))
	if err != nil {
		return operationresult.Result[context.Context]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[context.Context]{Status: current.Status, Message: current.Message}, nil
	}
	row, binding := current.Data.Session, current.Data.Binding
	if agentshare.IsBackgroundSession(row) {
		if !idleOnly {
			return operationresult.Result[context.Context]{Status: operationresult.NotFound, Message: "目标不存在"}, nil
		}
		return operationresult.Result[context.Context]{Status: operationresult.InvalidTarget, Message: "rollback is only available in foreground sessions"}, nil
	}
	if original, ok := session.BindingFromContext(ctx); ok && original != binding {
		return operationresult.Result[context.Context]{Status: operationresult.Conflict, Message: "rollback session expired"}, nil
	}
	if idleOnly && (p.turns.Snapshot(row.ID).Phase != turn.PhaseIdle || agentshare.CompactActive(p.turns, p.requests, row.ID)) {
		return operationresult.Result[context.Context]{Status: operationresult.Busy, Message: "会话正忙"}, nil
	}
	ctx = session.WithBinding(contextinfo.WithActor(ctx, p.identity.Actor(ctx)), binding)
	// Listing never enters commit admission, but a reused command context must
	// still reject a write if a Turn begins while it waits for the target lock.
	ctx = agentshare.FileRollbackContext(ctx, p.service, p.sessions, p.turns, p.requests, p.identity, row, true)
	return operationresult.Result[context.Context]{Status: operationresult.Success, Data: workspace.WithWorkspaceStore(ctx, p.NewWorkspaceStore(row.ID))}, nil
}

// FileCommandPreparerDeps declares only the capabilities consumed by FileCommandPreparer.
type FileCommandPreparerDeps struct {
	Service interface {
		WithBinding(context.Context, fileops.Binding, ...fileops.CommitAdmission) context.Context
	}
	Sessions interface {
		CurrentBound(context.Context, session.Scope) (operationresult.Result[session.CurrentBoundData], error)
		EnterBinding(context.Context, *session.Binding) (context.Context, func(), error)
		EnterSessions(context.Context, ...string) (context.Context, func(), error)
	}
	NewWorkspaceStore func(string) workspace.WorkspaceStore
	Turns             interface{ Snapshot(string) turn.Snapshot }
	Requests          interface {
		ListBySession(string) []request.Request
	}
	Identity interface {
		Scope(context.Context) session.Scope
		Actor(context.Context) contextinfo.Actor
	}
}

// NewFileCommandPreparer initializes FileCommandPreparer without installing cross-service callbacks.
func NewFileCommandPreparer(deps FileCommandPreparerDeps) *FileCommandPreparer {
	return &FileCommandPreparer{
		service:           deps.Service,
		sessions:          deps.Sessions,
		NewWorkspaceStore: deps.NewWorkspaceStore,
		turns:             deps.Turns,
		requests:          deps.Requests,
		identity:          deps.Identity,
	}
}
