package modelmgr

import (
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/signal"
)

// ModelRetryingEvent is emitted with the actual provider call's context. That
// context expires when the response stream ends, even if its parent survives.
type ModelRetryingEvent struct {
	// Identity is captured by the publishing source from this provider call;
	// nil fields are absent, and published pointers are read-only snapshots.
	Identity events.LogIdentity
	// Provider identifies the configured provider that owns the retrying client.
	Provider string
	// Retry retains the transport's explicit attempt result and original error.
	Retry llm.RetryEvent
}

func (s *Service) ModelRetrying() *signal.Signal[ModelRetryingEvent] { return s.retrying }
