package agent

import (
	"context"
	"fmt"

	"elbot/internal/contextinfo"
	"elbot/internal/fileops"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

type fileCommandPreparer struct {
	service     *fileops.Service
	sessions    *session.Service
	sessionRows storage.SessionRepository
	turns       *turn.Manager
	requests    *request.Manager
	identity    *identityResolver
}

func (a *Agent) PrepareFileCommand(ctx context.Context, idleOnly bool) (context.Context, error) {
	return a.fileCommands.PrepareFileCommand(ctx, idleOnly)
}

func fileRollbackContext(ctx context.Context, service *fileops.Service, sessions *session.Service, turns *turn.Manager, requests *request.Manager, identity *identityResolver, row *storage.Session, idleOnly ...bool) context.Context {
	if row == nil || service == nil {
		return ctx
	}
	if isBackgroundSession(row) {
		return service.WithBinding(ctx, nil, func(ctx context.Context) (context.Context, func(), error) {
			return sessions.EnterSessions(ctx, row.ID)
		})
	}
	binding, ok := session.BindingFromContext(ctx)
	if !ok {
		_, current, err := sessions.CurrentBound(ctx, identity.Scope(ctx))
		if err == nil && current.SessionID() == row.ID {
			binding = current
		}
	}
	return service.WithBinding(ctx, binding, func(ctx context.Context) (context.Context, func(), error) {
		locked, release, err := sessions.EnterBinding(ctx, binding)
		if err != nil {
			return ctx, nil, err
		}
		if len(idleOnly) > 0 && idleOnly[0] && (turns.Snapshot(row.ID).Phase != turn.PhaseIdle || compactActive(turns, requests, row.ID)) {
			release()
			return ctx, nil, fmt.Errorf("当前会话仍在执行任务或压缩；请等待完成，或先 /stop")
		}
		return locked, release, nil
	})
}

// PrepareFileCommand captures the original binding and execution admission for
// file commands. The file service owns record lookup and the actual operation.
func (p *fileCommandPreparer) PrepareFileCommand(ctx context.Context, idleOnly bool) (context.Context, error) {
	if p.identity.Actor(ctx).Role != contextinfo.RoleSuperadmin {
		return ctx, fmt.Errorf("rollback requires superadmin role")
	}
	if p.service == nil {
		return ctx, fmt.Errorf("file rollback is not configured")
	}
	row, binding, err := p.sessions.CurrentBound(ctx, p.identity.Scope(ctx))
	if err != nil {
		return ctx, err
	}
	if isBackgroundSession(row) {
		if !idleOnly {
			return ctx, storage.ErrNotFound
		}
		return ctx, fmt.Errorf("rollback is only available in foreground sessions")
	}
	if original, ok := session.BindingFromContext(ctx); ok && original != binding {
		return ctx, fileops.ErrRollbackExpired
	}
	if idleOnly && (p.turns.Snapshot(row.ID).Phase != turn.PhaseIdle || compactActive(p.turns, p.requests, row.ID)) {
		return ctx, fmt.Errorf("当前会话仍在执行任务或压缩；请等待完成，或先 /stop")
	}
	ctx = session.WithBinding(contextinfo.WithActor(ctx, p.identity.Actor(ctx)), binding)
	// Listing never enters commit admission, but a reused command context must
	// still reject a write if a Turn begins while it waits for the target lock.
	ctx = fileRollbackContext(ctx, p.service, p.sessions, p.turns, p.requests, p.identity, row, true)
	return workspace.WithWorkspaceStore(ctx, session.NewWorkspaceStore(p.sessions, p.sessionRows, row.ID)), nil
}
