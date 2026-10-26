package share

import (
	"context"
	"fmt"

	"elbot/internal/fileops"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func FileRollbackContext(ctx context.Context, service interface {
	WithBinding(context.Context, fileops.Binding, ...fileops.CommitAdmission) context.Context
}, sessions interface {
	CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error)
	EnterBinding(context.Context, *session.Binding) (context.Context, func(), error)
	EnterSessions(context.Context, ...string) (context.Context, func(), error)
}, turns interface{ Snapshot(string) turn.Snapshot }, requests interface {
	ListBySession(string) []request.Request
}, identity interface {
	Scope(context.Context) session.Scope
}, row *storage.Session, idleOnly ...bool) context.Context {
	if row == nil {
		return ctx
	}
	if IsBackgroundSession(row) {
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
		if len(idleOnly) > 0 && idleOnly[0] && (turns.Snapshot(row.ID).Phase != turn.PhaseIdle || CompactActive(turns, requests, row.ID)) {
			release()
			return ctx, nil, fmt.Errorf("当前会话仍在执行任务或压缩；请等待完成，或先 /stop")
		}
		return locked, release, nil
	})
}
