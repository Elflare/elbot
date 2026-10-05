package agent

import (
	"context"
	"fmt"

	"elbot/internal/fileops"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

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
func (a *Agent) PrepareFileCommand(ctx context.Context, idleOnly bool) (context.Context, error) {
	if a.identity.Actor(ctx).Role != security.RoleSuperadmin {
		return ctx, fmt.Errorf("rollback requires superadmin role")
	}
	if a.toolRuntime.fileRollback == nil {
		return ctx, fmt.Errorf("file rollback is not configured")
	}
	row, binding, err := a.sessions.CurrentBound(ctx, a.identity.Scope(ctx))
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
	if idleOnly && (a.turns.Snapshot(row.ID).Phase != turn.PhaseIdle || a.execution.compactActive(row.ID)) {
		return ctx, fmt.Errorf("当前会话仍在执行任务或压缩；请等待完成，或先 /stop")
	}
	ctx = session.WithBinding(security.WithActor(ctx, a.identity.Actor(ctx)), binding)
	// Listing never enters commit admission, but a reused command context must
	// still reject a write if a Turn begins while it waits for the target lock.
	ctx = fileRollbackContext(ctx, a.toolRuntime.fileRollback, a.sessions, a.turns, a.requests, a.identity, row, true)
	return workspace.WithWorkspaceStore(ctx, a.workspaceStore(row)), nil
}

func (a *Agent) workspaceStore(row *storage.Session) *session.WorkspaceStore {
	return session.NewWorkspaceStore(a.sessions, a.store.Sessions(), row.ID)
}
