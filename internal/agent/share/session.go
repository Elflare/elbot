package share

import (
	"context"
	"errors"

	"elbot/internal/request"
	"elbot/internal/session"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

var ErrSessionBindingChanged = errors.New("当前会话已切换，请重新发送消息")

// Existing execution contexts keep their original binding across confirmation.
func CaptureSessionBinding(ctx context.Context, sessions *session.Service, identity interface {
	Scope(context.Context) session.Scope
}, row *storage.Session) (context.Context, error) {
	current, binding, err := sessions.CurrentBound(ctx, identity.Scope(ctx))
	if err != nil {
		return ctx, err
	}
	if current.ID != row.ID {
		return ctx, ErrSessionBindingChanged
	}
	if original, ok := session.BindingFromContext(ctx); ok {
		if original != binding || !original.Valid() {
			return ctx, ErrSessionBindingChanged
		}
		return ctx, nil
	}
	return session.WithBinding(ctx, binding), nil
}

func CompactActive(turns *turn.Manager, requests *request.Manager, sessionID string) bool {
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

func IsBackgroundSession(row *storage.Session) bool { return sessionpkg.IsBackground(row) }
