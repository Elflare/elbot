package share

import (
	"context"
	"elbot/internal/operationresult"

	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// Existing execution contexts keep their original binding across confirmation.
func CaptureSessionBinding(ctx context.Context, sessions interface {
	CurrentBound(context.Context, session.Scope) (operationresult.Result[session.CurrentBoundData], error)
}, identity interface {
	Scope(context.Context) session.Scope
}, id string) (context.Context, operationresult.Result[*storage.Session], error) {
	current, err := sessions.CurrentBound(ctx, identity.Scope(ctx))
	if err != nil {
		return ctx, operationresult.Result[*storage.Session]{}, err
	}
	if current.Status != operationresult.Success {
		return ctx, operationresult.Result[*storage.Session]{Status: current.Status, Message: current.Message}, nil
	}
	binding := current.Data.Binding
	if current.Data.Session.ID != id {
		return ctx, operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "当前会话已切换，请重新发送消息"}, nil
	}
	if original, ok := session.BindingFromContext(ctx); ok {
		if original != binding || !original.Valid() {
			return ctx, operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "当前会话已切换，请重新发送消息"}, nil
		}
	} else {
		ctx = session.WithBinding(ctx, binding)
	}
	return ctx, operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: current.Data.Session}, nil
}

func CompactActive(turns interface {
	Snapshot(string) turn.Snapshot
}, requests interface {
	ListBySession(string) []request.Request
}, sessionID string) bool {
	if turns.Snapshot(sessionID).Phase == turn.PhaseCompact {
		return true
	}
	for _, active := range requests.ListBySession(sessionID) {
		if active.Kind == request.KindCompress {
			return true
		}
	}
	return false
}

func IsBackgroundSession(row *storage.Session) bool { return session.IsBackground(row) }
