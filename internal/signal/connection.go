package signal

import "sync"

// Connection disconnects future emissions, not existing snapshots or jobs.
type Connection struct {
	once       sync.Once
	disconnect func()
}

func (c *Connection) Disconnect() {
	if c != nil {
		c.once.Do(c.disconnect)
	}
}
