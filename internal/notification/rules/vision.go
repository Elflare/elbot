package rules

import (
	"context"
	"sync"

	"elbot/internal/delivery"
	"elbot/internal/session"
)

type AssistantSender interface {
	SendAssistant(context.Context, string) (delivery.Receipt, error)
}

// VisionNotices owns the session-level once-per-send-attempt policy. Scheduling
// or canceling a queued hint never consumes the session's first attempt.
type VisionNotices struct {
	mu        sync.Mutex
	attempted map[string]bool
	sender    AssistantSender
}

func NewVisionNotices(sender AssistantSender) *VisionNotices {
	return &VisionNotices{attempted: make(map[string]bool), sender: sender}
}
func (v *VisionNotices) Send(ctx context.Context, sessionID string, visible bool) error {
	if !visible || ctx.Err() != nil {
		return nil
	}
	if binding, ok := session.BindingFromContext(ctx); ok && !binding.Valid() {
		return nil
	}
	v.mu.Lock()
	if v.attempted[sessionID] {
		v.mu.Unlock()
		return nil
	}
	v.attempted[sessionID] = true
	v.mu.Unlock()
	_, err := v.sender.SendAssistant(ctx, VisionFallback)
	return err
}
