package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"elbot/internal/modelmgr"
)

type wiringLifecycle struct {
	components *WiringComponents
	models     *modelmgr.Service
	done       chan struct{}
	mu         sync.Mutex
	closed     bool
}

func (l *wiringLifecycle) groups() []struct {
	name  string
	owner ModuleLifecycle
} {
	var groups []struct {
		name  string
		owner ModuleLifecycle
	}
	c := l.components
	if c.Platform != nil {
		groups = append(groups, struct {
			name  string
			owner ModuleLifecycle
		}{"platform", c.Platform})
	}
	if c.Background != nil {
		groups = append(groups, struct {
			name  string
			owner ModuleLifecycle
		}{"background", c.Background})
	}
	if c.Dialogue != nil {
		groups = append(groups, struct {
			name  string
			owner ModuleLifecycle
		}{"dialogue", c.Dialogue})
	}
	if c.Hook != nil {
		groups = append(groups, struct {
			name  string
			owner ModuleLifecycle
		}{"hook", c.Hook})
	}
	if c.Tool != nil {
		groups = append(groups, struct {
			name  string
			owner ModuleLifecycle
		}{"tool", c.Tool})
	}
	return groups
}
func (l *wiringLifecycle) BeginClose() {
	for _, g := range l.groups() {
		g.owner.BeginClose()
	}
}
func (l *wiringLifecycle) Done() <-chan struct{} { return l.done }
func (l *wiringLifecycle) Close(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.BeginClose()
	var errs []error
	for _, g := range l.groups() {
		if err := g.owner.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", g.name, err))
		}
		select {
		case <-g.owner.Done():
		default:
			return errors.Join(errs...)
		}
	}
	if l.models != nil {
		if err := l.models.Close(ctx); err != nil {
			return errors.Join(append(errs, err)...)
		}
	}
	l.closed = true
	close(l.done)
	return errors.Join(errs...)
}
