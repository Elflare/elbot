package media

import (
	"context"
	"sync"
)

// objectLocks serializes mutations of one content ID without blocking other IDs.
// Entries count both holders and waiters so an ID cannot acquire two locks.
type objectLocks struct {
	mu      sync.Mutex
	entries map[string]*objectLock
}

type objectLock struct {
	token chan struct{}
	users int
}

func (l *objectLocks) acquire(ctx context.Context, id string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.entries == nil {
		l.entries = make(map[string]*objectLock)
	}
	entry := l.entries[id]
	if entry == nil {
		entry = &objectLock{token: make(chan struct{}, 1)}
		l.entries[id] = entry
	}
	entry.users++
	l.mu.Unlock()

	drop := func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		entry.users--
		if entry.users == 0 {
			delete(l.entries, id)
		}
	}
	select {
	case entry.token <- struct{}{}:
		unlock := func() {
			<-entry.token
			drop()
		}
		if err := ctx.Err(); err != nil {
			unlock()
			return nil, err
		}
		return unlock, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}
