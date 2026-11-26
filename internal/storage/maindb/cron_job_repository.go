package maindb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"elbot/internal/storage/share"
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

type UpsertCronJobRequest struct {
	Name          string
	Handler       string
	Schedule      string
	Enabled       bool
	Metadata      string
	NextRunAt     *time.Time
	ResetDelivery bool
}

func (r *Store) UpsertCronJob(ctx context.Context, req UpsertCronJobRequest) (*CronJob, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin cron upsert: %w", err)
	}
	defer tx.Rollback()
	now := share.FormatTime(time.Now())
	// Take the write lock before reading. An unchanged definition must not refresh updated_at.
	_, err = tx.ExecContext(ctx, `INSERT INTO cron_jobs
(id, name, handler, schedule, enabled, metadata, next_run_at, run_count, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?) ON CONFLICT(name) DO NOTHING`,
		share.NewID(), req.Name, req.Handler, req.Schedule, req.Enabled, share.NullString(req.Metadata), nullTime(req.NextRunAt), now, now)
	if err != nil {
		return nil, fmt.Errorf("insert cron job: %w", err)
	}
	job, err := scanCronJob(tx.QueryRowContext(ctx, `SELECT `+cronJobColumns+` FROM cron_jobs WHERE name = ?`, req.Name))
	if err != nil {
		return nil, fmt.Errorf("read cron definition: %w", err)
	}
	if job.Handler != req.Handler || job.Schedule != req.Schedule || job.Enabled != req.Enabled || job.Metadata != req.Metadata ||
		!optionalTimeEqual(job.NextRunAt, req.NextRunAt) || req.ResetDelivery && (job.DeliveryToken != "" || job.DeliveryState != "") {
		reset := ""
		if req.ResetDelivery {
			reset = ", delivery_state=NULL, delivery_token=NULL"
		}
		job, err = scanCronJob(tx.QueryRowContext(ctx, `UPDATE cron_jobs SET handler=?, schedule=?, enabled=?, metadata=?,
next_run_at=?, updated_at=?`+reset+` WHERE id=? RETURNING `+cronJobColumns,
			req.Handler, req.Schedule, req.Enabled, share.NullString(req.Metadata), nullTime(req.NextRunAt), now, job.ID))
		if err != nil {
			return nil, fmt.Errorf("update cron definition: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit cron upsert: %w", err)
	}
	return job, nil
}

func optionalTimeEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

const cronJobColumns = `id, name, handler, schedule, enabled, metadata, last_run_at, next_run_at,
       run_count, created_at, updated_at, delivery_state, delivery_token`

func (r *Store) GetByName(ctx context.Context, name string) (*CronJob, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+cronJobColumns+` FROM cron_jobs WHERE name = ?`, name)
	job, err := scanCronJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, share.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get cron job: %w", err)
	}
	return job, nil
}

func (r *Store) ListCronJobs(ctx context.Context, includeDisabled bool) ([]CronJob, error) {
	query := `SELECT ` + cronJobColumns + ` FROM cron_jobs`
	if !includeDisabled {
		query += `
WHERE enabled = 1`
	}
	query += `
ORDER BY name`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list cron jobs: %w", err)
	}
	defer rows.Close()

	jobs := []CronJob{}
	for rows.Next() {
		job, err := scanCronJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan cron job: %w", err)
		}
		jobs = append(jobs, *job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cron jobs: %w", err)
	}
	return jobs, nil
}

func (r *Store) UpdateNextRunAt(ctx context.Context, id string, nextRunAt *time.Time, updatedAt time.Time) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE cron_jobs
SET next_run_at = ?, updated_at = ?
WHERE id = ?`,
		nullTime(nextRunAt),
		share.FormatTime(updatedAt),
		id,
	)
	return requireRow(res, err, "update cron job next run")
}

func (r *Store) UpdateRunState(ctx context.Context, id string, lastRunAt, updatedAt time.Time) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE cron_jobs
SET last_run_at = ?, run_count = run_count + 1, updated_at = ?
WHERE id = ?`,
		share.FormatTime(lastRunAt),
		share.FormatTime(updatedAt),
		id,
	)
	return requireRow(res, err, "update cron job run state")
}

func (r *Store) CompareAndSwapDelivery(ctx context.Context, id, expectedToken, nextToken, deliveryState string, mappings []PlatformMessageMap) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin cron delivery commit: %w", err)
	}
	defer tx.Rollback()
	for _, mapping := range mappings {
		if err := mapPlatformMessage(ctx, tx, mapping); err != nil {
			return false, err
		}
	}
	res, err := tx.ExecContext(ctx, `
UPDATE cron_jobs
SET delivery_state = ?, delivery_token = ?, updated_at = ?
WHERE id = ? AND enabled = 1 AND COALESCE(delivery_token, '') = ?`,
		share.NullString(deliveryState),
		share.NullString(nextToken),
		share.FormatTime(time.Now()),
		id,
		expectedToken,
	)
	if err != nil {
		return false, fmt.Errorf("compare and swap cron delivery: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("compare and swap cron delivery rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit cron delivery: %w", err)
	}
	return n > 0, nil
}

func (r *Store) DisableByName(ctx context.Context, name string) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE cron_jobs
SET enabled = 0, next_run_at = NULL, updated_at = ?
WHERE name = ?`, share.FormatTime(time.Now()), name)
	return requireRow(res, err, "disable cron job")
}

func (r *Store) DisableByNameIfDeliveryToken(ctx context.Context, name, deliveryToken string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
UPDATE cron_jobs
SET enabled = 0, next_run_at = NULL, updated_at = ?
WHERE name = ? AND enabled = 1 AND COALESCE(delivery_token, '') = ?`,
		share.FormatTime(time.Now()), name, deliveryToken)
	if err != nil {
		return false, fmt.Errorf("disable cron job by delivery token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("disable cron job by delivery token rows: %w", err)
	}
	return n > 0, nil
}

func (r *Store) DeleteByName(ctx context.Context, name string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM cron_jobs WHERE name = ?`, name)
	return requireRow(res, err, "delete cron job")
}

func scanCronJob(row interface{ Scan(dest ...any) error }) (*CronJob, error) {
	var job CronJob
	var enabled int
	var metadata, deliveryState, deliveryToken, lastRunAt, nextRunAt sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&job.ID,
		&job.Name,
		&job.Handler,
		&job.Schedule,
		&enabled,
		&metadata,
		&lastRunAt,
		&nextRunAt,
		&job.RunCount,
		&createdAt,
		&updatedAt,
		&deliveryState,
		&deliveryToken,
	); err != nil {
		return nil, err
	}
	job.Enabled = enabled != 0
	job.Metadata = metadata.String
	job.DeliveryState = deliveryState.String
	job.DeliveryToken = deliveryToken.String

	parsedCreatedAt, err := share.ParseTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse cron job created_at: %w", err)
	}
	job.CreatedAt = parsedCreatedAt
	parsedUpdatedAt, err := share.ParseTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse cron job updated_at: %w", err)
	}
	job.UpdatedAt = parsedUpdatedAt
	job.LastRunAt, err = share.ParseOptionalTime(lastRunAt.String)
	if err != nil {
		return nil, fmt.Errorf("parse cron job last_run_at: %w", err)
	}
	job.NextRunAt, err = share.ParseOptionalTime(nextRunAt.String)
	if err != nil {
		return nil, fmt.Errorf("parse cron job next_run_at: %w", err)
	}
	return &job, nil
}
