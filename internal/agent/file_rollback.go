package agent

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
	"elbot/internal/utils/fileops"
)

func (a *Agent) fileRollbackContext(ctx context.Context, session *storage.Session) context.Context {
	if session == nil || isBackgroundSession(session) || a.toolRuntime.fileRollback == nil {
		return ctx
	}
	return a.toolRuntime.fileRollback.WithSession(ctx, a.scope(ctx).Key(), session.ID)
}

func (a *Agent) ListFileRollbacks(ctx context.Context) ([]fileops.RollbackInfo, error) {
	if a.actor(ctx).Role != security.RoleSuperadmin {
		return nil, fmt.Errorf("rollback requires superadmin role")
	}
	if a.toolRuntime.fileRollback == nil {
		return nil, fmt.Errorf("file rollback is not configured")
	}
	session, err := a.sessions.Current(ctx, a.scope(ctx))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if isBackgroundSession(session) {
		return nil, nil
	}
	ctx = a.fileRollbackContext(security.WithActor(ctx, a.actor(ctx)), session)
	return a.toolRuntime.fileRollback.List(ctx)
}

func (a *Agent) RollbackFile(ctx context.Context, id uint64) (fileops.RollbackResult, error) {
	if a.actor(ctx).Role != security.RoleSuperadmin {
		return fileops.RollbackResult{}, fmt.Errorf("rollback requires superadmin role")
	}
	if a.toolRuntime.fileRollback == nil {
		return fileops.RollbackResult{}, fmt.Errorf("file rollback is not configured")
	}
	session, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil {
		return fileops.RollbackResult{}, err
	}
	if a.turns.Snapshot(session.ID).Phase != turn.PhaseIdle || a.compactActive(session.ID) {
		return fileops.RollbackResult{}, fmt.Errorf("当前会话仍在执行任务或压缩；请等待完成，或先 /stop")
	}
	ctx = a.fileRollbackContext(security.WithActor(ctx, a.actor(ctx)), session)
	ctx = tool.WithWorkspaceStore(ctx, sessionWorkspaceStore{agent: a, session: session})
	records, err := a.toolRuntime.fileRollback.List(ctx)
	if err != nil {
		return fileops.RollbackResult{}, err
	}
	for _, record := range records {
		if record.ID != id {
			continue
		}
		result, err := a.toolRuntime.fileRollback.Rollback(ctx, record.Path, id)
		if err != nil {
			a.audit("file_rollback_failed", "actor_id", a.actor(ctx).ID, "session_id", session.ID, "path", record.Path, "error", err.Error())
			return fileops.RollbackResult{}, err
		}
		a.audit("file_rollback", "actor_id", a.actor(ctx).ID, "session_id", session.ID, "path", result.Path, "deleted", result.Deleted)
		return result, nil
	}
	return fileops.RollbackResult{}, fileops.ErrRollbackNotFound
}
