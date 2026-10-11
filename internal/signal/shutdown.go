package signal

import "context"

type ShutdownPolicy uint8

const (
	CancelPending ShutdownPolicy = iota
	Drain
)

// Task describes one bounded unit of work.
type Task struct {
	Run      func(context.Context) error
	Shutdown ShutdownPolicy
	Key      string             // Equal nonempty keys cannot execute concurrently.
	Finish   func(error)        // Called once, including cancellation before execution; must not block.
	cancel   context.CancelFunc // Signal-owned context; the queue cancels it on shutdown, Finish owns normal release.
}
