package storage

import (
	"context"
	"time"
)

type CronJob struct {
	ID            string
	Name          string
	Handler       string
	Schedule      string
	Enabled       bool
	Metadata      string
	DeliveryState string
	DeliveryToken string
	LastRunAt     *time.Time
	NextRunAt     *time.Time
	RunCount      int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type CronJobRunState struct {
	LastRunAt time.Time // 本次执行开始时间，执行结束后记录。
	UpdatedAt time.Time // 本次运行事实的记录时间；次数由仓库原子递增。
}

type UpsertCronJobRequest struct {
	Name          string
	Handler       string
	Schedule      string
	Enabled       bool
	Metadata      string
	NextRunAt     *time.Time
	ResetDelivery bool
}

type CronJobRepository interface {
	UpsertCronJob(ctx context.Context, req UpsertCronJobRequest) (*CronJob, error)
	GetByName(ctx context.Context, name string) (*CronJob, error)
	ListCronJobs(ctx context.Context, includeDisabled bool) ([]CronJob, error)
	UpdateNextRunAt(ctx context.Context, id string, nextRunAt *time.Time, updatedAt time.Time) error
	UpdateRunState(ctx context.Context, id string, state CronJobRunState) error
	// CompareAndSwapDelivery saves actual sent-message mappings atomically with the
	// conditional progress update. A stale token preserves mappings but changes no progress.
	CompareAndSwapDelivery(ctx context.Context, id, expectedToken, nextToken, deliveryState string, mappings []PlatformMessageMap) (bool, error)
	DisableByName(ctx context.Context, name string) error
	DisableByNameIfDeliveryToken(ctx context.Context, name, deliveryToken string) (bool, error)
	DeleteByName(ctx context.Context, name string) error
}
