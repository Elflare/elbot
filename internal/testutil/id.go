package testutil

import (
	"context"
	"elbot/internal/storage"
	"sync/atomic"
)

var n atomic.Int64

// IDSequence supplies process-unique in-memory IDs to tests without a database.
func IDSequence() storage.IDSequence {
	return func(ctx context.Context) (int64, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return n.Add(1), nil
	}
}
