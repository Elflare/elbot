package modelmgr

import (
	"elbot/internal/llm"
	"elbot/internal/signal"
)

// ModelRetryingEvent is emitted with the actual provider call's context. That
// context expires when the response stream ends, even if its parent survives.
type ModelRetryingEvent struct {
	Provider string
	Retry    llm.RetryEvent
}

func (s *Service) ModelRetrying() *signal.Signal[ModelRetryingEvent] { return s.retrying }
