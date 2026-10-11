package modelmgr

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/signal"
)

// ModelRetryingEvent is emitted with the actual provider call's context. That
// context expires when the response stream ends, even if its parent survives.
type ModelRetryingEvent struct {
	// CallContext retains the actual provider call lifetime for transient UI
	// notifications. The fact itself must still be logged after cancellation.
	CallContext context.Context
	// Identity is captured by the publishing source from this provider call;
	// nil fields are absent, and published pointers are read-only snapshots.
	Identity events.LogIdentity
	// Provider identifies the configured provider that owns the retrying client.
	Provider string
	// Retry retains the transport's explicit attempt result and original error.
	Retry llm.RetryEvent
}

func (s *Service) ModelRetrying() *signal.Signal[ModelRetryingEvent, signal.Empty] { return s.retrying }
