package signal

import (
	"context"
	"sync"
)

// Connection disconnects future emissions, not existing snapshots or jobs.
type Connection struct {
	once       sync.Once
	disconnect func()
	mu         sync.Mutex
	tasks      map[*connectionTask]struct{}
	shutdown   ShutdownPolicy // Drain retains accepted callbacks until they actually return.
	closed     bool
	done       chan struct{}
}

type connectionTask struct{ cancel context.CancelFunc }

func (c *Connection) acquire(cancel context.CancelFunc) *connectionTask {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	t := &connectionTask{cancel: cancel}
	c.tasks[t] = struct{}{}
	return t
}
func (c *Connection) release(t *connectionTask) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tasks, t)
	if c.closed && len(c.tasks) == 0 {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	}
}

// BeginClose stops admission and applies the subscription shutdown policy.
// Disconnect alone retains existing snapshots and accepted work.
func (c *Connection) BeginClose() {
	if c == nil {
		return
	}
	c.Disconnect()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.shutdown == CancelPending {
		for t := range c.tasks {
			t.cancel()
		}
	}
	if len(c.tasks) == 0 {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	}
}
func (c *Connection) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.BeginClose()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *Connection) Done() <-chan struct{} { return c.done }

func (c *Connection) Disconnect() {
	if c != nil {
		c.once.Do(c.disconnect)
	}
}
