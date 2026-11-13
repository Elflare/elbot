package share

import (
	"context"

	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func FileRollbackContext(ctx context.Context, service interface {
	WithBinding(context.Context, fileops.Binding, ...fileops.CommitAdmission) context.Context
}, sessions interface {
	CurrentBound(context.Context, session.Scope) (operationresult.Result[session.CurrentBoundData], error)
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
		return service.WithBinding(ctx, nil, func(ctx context.Context) (operationresult.Result[fileops.CommitAdmissionData], error) {
			locked, release, err := sessions.EnterSessions(ctx, row.ID)
			if err != nil {
				return operationresult.Result[fileops.CommitAdmissionData]{}, err
			}
			return operationresult.Result[fileops.CommitAdmissionData]{Status: operationresult.Success, Data: fileops.CommitAdmissionData{Context: locked, Release: release}}, nil
		})
	}
	binding, ok := session.BindingFromContext(ctx)
	if !ok {
		current, err := sessions.CurrentBound(ctx, identity.Scope(ctx))
		if err == nil && current.Status == operationresult.Success && current.Data.Session.ID == row.ID {
			binding = current.Data.Binding
		}
	}
	return service.WithBinding(ctx, binding, func(ctx context.Context) (operationresult.Result[fileops.CommitAdmissionData], error) {
		locked, release, err := sessions.EnterBinding(ctx, binding)
		if err != nil {
			return operationresult.Result[fileops.CommitAdmissionData]{}, err
		}
		if len(idleOnly) > 0 && idleOnly[0] && (turns.Snapshot(row.ID).Phase != turn.PhaseIdle || CompactActive(turns, requests, row.ID)) {
			release()
			return operationresult.Result[fileops.CommitAdmissionData]{Status: operationresult.Busy, Message: "会话正忙"}, nil
		}
		return operationresult.Result[fileops.CommitAdmissionData]{Status: operationresult.Success, Data: fileops.CommitAdmissionData{Context: locked, Release: release}}, nil
	})
}
