package events

import "elbot/internal/signal"

// PlatformConnectedEvent describes a connection, without chat or actor identity.
// Platform is the publishing adapter's nonempty Name.
type PlatformConnectedEvent struct{ Platform string }

// PlatformConnected is stable for the process lifetime and does not replay events.
// Publishers emit directly; consumers own their subscriptions and execution.
var PlatformConnected = signal.New[PlatformConnectedEvent]("platform.connected")
