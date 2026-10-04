package signal

import "context"

// Executor accepts work without implying that the work has completed.
type Executor interface {
	Submit(context.Context, Task) error
}

type Lifetime uint8

const (
	FollowEmit Lifetime = iota + 1
	FollowExecutor
)

type ConnectOptions struct {
	Shutdown ShutdownPolicy
	Executor Executor
	Once     bool
	Lifetime Lifetime
}
