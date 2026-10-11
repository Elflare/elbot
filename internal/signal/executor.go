package signal

import (
	"context"
	"time"
)

// Empty is the result of observers without a business return value.
type Empty struct{}

// Options belong to a signal, never to a shared global pool.
type Options[T any] struct {
	Capacity    int                            // Maximum waiting callbacks, excluding running callbacks.
	Concurrency int                            // Maximum running callbacks; defaults to eight.
	Timeout     time.Duration                  // Admission and execution budget; defaults to 30 seconds.
	Required    bool                           // Emitting without a receiver is an error.
	Single      bool                           // At most one receiver, for request/notification routing.
	Key         func(T) string                 // Equal nonempty keys execute in admission order.
	Before      func(context.Context, T) error // Owner's execution-time guard, after waiting and before the receiver.
}
type ConnectOptions struct {
	Shutdown ShutdownPolicy // CancelPending or Drain when the signal closes.
	Once     bool           // Claim once, including a rejected admission.
}
