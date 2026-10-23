package share

import (
	"elbot/internal/contextinfo"
	"elbot/internal/hook"
	"elbot/internal/storage"
)

func HookSession(session *storage.Session) hook.SessionContext {
	if session == nil {
		return hook.SessionContext{}
	}
	return hook.SessionContext{ID: session.ID, Mode: session.Mode, Title: session.Title, Status: session.Status}
}

func ActorContext(actor contextinfo.Actor) hook.ActorContext {
	return hook.ActorContext{
		ID:          actor.ID,
		Role:        string(actor.Role),
		GroupRole:   string(actor.GroupRole),
		UserID:      actor.PlatformUserID,
		Nickname:    actor.Nickname,
		GroupCard:   actor.GroupCard,
		DisplayName: actor.DisplayName,
	}
}
