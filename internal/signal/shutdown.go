package signal

import "context"

type ShutdownPolicy uint8

const (
	CancelPending ShutdownPolicy = iota
	Drain
)

// Task keeps shutdown policy attached to the subscription even on shared queues.
type Task struct {
	Run      func(context.Context) error
	Shutdown ShutdownPolicy
}
