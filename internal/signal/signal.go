// Package signal implements typed, explicitly owned connections and executors.
// Publishers own payload snapshots. The signal does not copy mutable payloads.
package signal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

type Handler[T any] func(context.Context, T) error

type slot[T any] struct {
	handler Handler[T]
	options ConnectOptions
}

type Signal[T any] struct {
	mu    sync.Mutex
	name  string
	slots []*slot[T]
}

func New[T any](name string) *Signal[T] {
	return &Signal[T]{name: name}
}

func (s *Signal[T]) Connect(handler Handler[T], options ConnectOptions) (*Connection, error) {
	if handler == nil {
		return nil, errors.New("signal: nil handler")
	}
	if options.Shutdown != CancelPending && options.Shutdown != Drain {
		return nil, errors.New("signal: invalid shutdown policy")
	}
	if options.Executor == nil && options.Shutdown != CancelPending {
		return nil, errors.New("signal: shutdown policy requires an executor")
	}
	if options.Executor != nil {
		if options.Lifetime != FollowEmit && options.Lifetime != FollowExecutor {
			return nil, errors.New("signal: asynchronous connection requires a lifetime")
		}
	} else if options.Lifetime != 0 {
		return nil, errors.New("signal: lifetime requires an executor")
	}
	entry := &slot[T]{handler: handler, options: options}
	s.mu.Lock()
	s.slots = append(s.slots, entry)
	s.mu.Unlock()
	return &Connection{disconnect: func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, candidate := range s.slots {
			if candidate == entry {
				copy(s.slots[i:], s.slots[i+1:])
				s.slots[len(s.slots)-1] = nil
				s.slots = s.slots[:len(s.slots)-1]
				return
			}
		}
	}}, nil
}

// Emit invokes or submits a registration-ordered snapshot. Concurrent Emit calls
// may interleave. A once connection is consumed even if submission fails.
func (s *Signal[T]) Emit(ctx context.Context, event T) error {
	s.mu.Lock()
	snapshot := append([]*slot[T](nil), s.slots...)
	kept := s.slots[:0]
	for _, entry := range s.slots {
		if !entry.options.Once {
			kept = append(kept, entry)
		}
	}
	clear(s.slots[len(kept):])
	s.slots = kept
	s.mu.Unlock()
	var errs []error
	for _, entry := range snapshot {
		var err error
		if entry.options.Executor == nil {
			err = entry.handler(ctx, event)
		} else {
			taskCtx := ctx
			if entry.options.Lifetime == FollowExecutor {
				taskCtx = context.WithoutCancel(ctx)
			}
			err = entry.options.Executor.Submit(taskCtx, Task{Shutdown: entry.options.Shutdown, Run: func(runCtx context.Context) error {
				if err := entry.handler(runCtx, event); err != nil {
					return fmt.Errorf("signal %s: %w", s.name, err)
				}
				return nil
			}})
		}
		if err != nil {
			unexpected, _ := filterErrors(err, func(leaf error) bool {
				return (entry.options.Executor != nil && entry.options.Shutdown != Drain && errors.Is(leaf, ErrClosed)) || (ctx.Err() != nil && errors.Is(leaf, ctx.Err()))
			})
			if unexpected != nil {
				reportFailure("signal dispatch failed", slog.String("signal", s.name), slog.Any("error", unexpected))
			}
			errs = append(errs, fmt.Errorf("signal %s: %w", s.name, err))
		}
	}
	return errors.Join(errs...)
}
