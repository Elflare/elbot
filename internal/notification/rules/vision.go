package rules

import (
	"context"
	"elbot/internal/events"
	"log/slog"
	"sync"
)

// VisionNotices reserves one attempt per session. Rejection/cancellation before
// the platform callback begins returns the reservation to the next visible turn.
type VisionNotices struct {
	mu        sync.Mutex
	attempted map[string]bool
}

func NewVisionNotices() *VisionNotices { return &VisionNotices{attempted: make(map[string]bool)} }
func (v *VisionNotices) Send(ctx context.Context, id string, visible bool) error {
	if !visible || ctx.Err() != nil {
		return nil
	}
	v.mu.Lock()
	if v.attempted[id] {
		v.mu.Unlock()
		return nil
	}
	v.attempted[id] = true
	v.mu.Unlock()
	results, err := events.NotificationRequested.Emit(ctx, events.NewNotification(ctx, slog.LevelWarn, VisionFallback))
	started := len(results) > 0 && results[0].Started
	if !started {
		v.mu.Lock()
		delete(v.attempted, id)
		v.mu.Unlock()
	}
	return err
}
