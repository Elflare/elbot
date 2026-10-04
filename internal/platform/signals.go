package platform

import "elbot/internal/signal"

// ConnectedEvent has no chat or sender: it describes a platform connection.
type ConnectedEvent struct{ Platform string }

type ConnectionSource interface {
	ConnectedSignal() *signal.Signal[ConnectedEvent]
}
