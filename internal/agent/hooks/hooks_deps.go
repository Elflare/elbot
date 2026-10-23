package hooks

import (
	"context"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/media"
	"elbot/internal/request"
	"elbot/internal/signal"
)

type HookIdentity interface {
	SourceActor(context.Context) contextinfo.Actor
}

// HookBridgeDeps declares only the capabilities consumed by HookBridge.
type HookBridgeDeps struct {
	Manager    hookRunner
	Router     HookRouter
	Requests   *request.Manager
	Identity   HookIdentity
	Media      *media.Manager
	Failed     *signal.Signal[agentevents.HookFailedEvent, signal.Empty]
	Dispatcher *dispatch.Router
}
