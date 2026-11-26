package storage

import (
	"context"
	"time"

	"elbot/internal/storage/maindb"
)

type (
	CronJob              = maindb.CronJob
	UpsertCronJobRequest = maindb.UpsertCronJobRequest
)

type CronJobRepository interface {
	UpsertCronJob(ctx context.Context, req UpsertCronJobRequest) (*CronJob, error)
	GetByName(ctx context.Context, name string) (*CronJob, error)
	ListCronJobs(ctx context.Context, includeDisabled bool) ([]CronJob, error)
	UpdateNextRunAt(ctx context.Context, id string, nextRunAt *time.Time, updatedAt time.Time) error
	UpdateRunState(ctx context.Context, id string, lastRunAt, updatedAt time.Time) error
	// CompareAndSwapDelivery saves actual sent-message mappings atomically with the
	// conditional progress update. A stale token preserves mappings but changes no progress.
	CompareAndSwapDelivery(ctx context.Context, id, expectedToken, nextToken, deliveryState string, mappings []PlatformMessageMap) (bool, error)
	DisableByName(ctx context.Context, name string) error
	DisableByNameIfDeliveryToken(ctx context.Context, name, deliveryToken string) (bool, error)
	DeleteByName(ctx context.Context, name string) error
}
