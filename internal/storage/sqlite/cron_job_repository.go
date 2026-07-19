package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"elbot/internal/storage"
)

type CronJobRepository struct {
	db *sql.DB
}

func (r *CronJobRepository) Upsert(ctx context.Context, req storage.UpsertCronJobRequest) (*storage.CronJob, error) {
	now := storage.Now()
	job, err := r.GetByName(ctx, req.Name)
	if errors.Is(err, storage.ErrNotFound) {
		job = &storage.CronJob{
			ID:        storage.NewID(),
			Name:      req.Name,
			Handler:   req.Handler,
			Schedule:  req.Schedule,
			Enabled:   req.Enabled,
			Metadata:  req.Metadata,
			NextRunAt: req.NextRunAt,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := r.create(ctx, job); err != nil {
			return nil, err
		}
		return job, nil
	}
	if err != nil {
		return nil, err
	}

	if cronJobMatchesUpsert(job, req) {
		return job, nil
	}

	job.Handler = req.Handler
	job.Schedule = req.Schedule
	job.Enabled = req.Enabled
	job.Metadata = req.Metadata
	job.NextRunAt = req.NextRunAt
	job.UpdatedAt = now
	if err := r.update(ctx, job, req.ResetDelivery); err != nil {
		return nil, err
	}
	return r.GetByName(ctx, req.Name)
}

func cronJobMatchesUpsert(job *storage.CronJob, req storage.UpsertCronJobRequest) bool {
	if job == nil {
		return false
	}
	if req.ResetDelivery && (job.DeliveryState != "" || job.DeliveryToken != "") {
		return false
	}
	return job.Handler == req.Handler &&
		job.Schedule == req.Schedule &&
		job.Enabled == req.Enabled &&
		job.Metadata == req.Metadata &&
		optionalTimeEqual(job.NextRunAt, req.NextRunAt)
}

func optionalTimeEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func (r *CronJobRepository) GetByName(ctx context.Context, name string) (*storage.CronJob, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, name, handler, schedule, enabled, metadata, last_run_at, next_run_at,
       run_count, last_error, created_at, updated_at, delivery_state, delivery_token
FROM cron_jobs
WHERE name = ?`, name)
	job, err := scanCronJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get cron job: %w", err)
	}
	return job, nil
}

func (r *CronJobRepository) List(ctx context.Context, includeDisabled bool) ([]storage.CronJob, error) {
	query := `
SELECT id, name, handler, schedule, enabled, metadata, last_run_at, next_run_at,
       run_count, last_error, created_at, updated_at, delivery_state, delivery_token
FROM cron_jobs`
	args := []any{}
	if !includeDisabled {
		query += `
WHERE enabled = 1`
	}
	query += `
ORDER BY name`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list cron jobs: %w", err)
	}
	defer rows.Close()

	jobs := []storage.CronJob{}
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

func (r *CronJobRepository) ListEnabled(ctx context.Context) ([]storage.CronJob, error) {
	return r.List(ctx, false)
}

func (r *CronJobRepository) UpdateNextRunAt(ctx context.Context, id string, nextRunAt *time.Time, updatedAt time.Time) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE cron_jobs
SET next_run_at = ?, updated_at = ?
WHERE id = ?`,
		nullTime(nextRunAt),
		storage.FormatTime(updatedAt),
		id,
	)
	if err != nil {
		return fmt.Errorf("update cron job next run: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *CronJobRepository) UpdateRunState(ctx context.Context, id string, state storage.CronJobRunState) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE cron_jobs
SET last_run_at = ?, next_run_at = ?, run_count = ?, last_error = ?, enabled = ?, updated_at = ?
WHERE id = ?`,
		storage.FormatTime(state.LastRunAt),
		nullTime(state.NextRunAt),
		state.RunCount,
		nullString(state.LastError),
		boolInt(state.Enabled),
		storage.FormatTime(state.UpdatedAt),
		id,
	)
	if err != nil {
		return fmt.Errorf("update cron job run state: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *CronJobRepository) CompareAndSwapDelivery(ctx context.Context, id, expectedToken, nextToken, deliveryState string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
UPDATE cron_jobs
SET delivery_state = ?, delivery_token = ?, updated_at = ?
WHERE id = ? AND enabled = 1 AND COALESCE(delivery_token, '') = ?`,
		nullString(deliveryState),
		nullString(nextToken),
		storage.FormatTime(storage.Now()),
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
	return n > 0, nil
}

func (r *CronJobRepository) DisableByName(ctx context.Context, name string) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE cron_jobs
SET enabled = 0, next_run_at = NULL, updated_at = ?
WHERE name = ?`, storage.FormatTime(storage.Now()), name)
	if err != nil {
		return fmt.Errorf("disable cron job: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *CronJobRepository) DisableByNameIfDeliveryToken(ctx context.Context, name, deliveryToken string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
UPDATE cron_jobs
SET enabled = 0, next_run_at = NULL, updated_at = ?
WHERE name = ? AND enabled = 1 AND COALESCE(delivery_token, '') = ?`,
		storage.FormatTime(storage.Now()), name, deliveryToken)
	if err != nil {
		return false, fmt.Errorf("disable cron job by delivery token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("disable cron job by delivery token rows: %w", err)
	}
	return n > 0, nil
}

func (r *CronJobRepository) DeleteByName(ctx context.Context, name string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM cron_jobs WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete cron job: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *CronJobRepository) create(ctx context.Context, job *storage.CronJob) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO cron_jobs (
    id, name, handler, schedule, enabled, metadata, last_run_at, next_run_at,
    run_count, last_error, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID,
		job.Name,
		job.Handler,
		job.Schedule,
		boolInt(job.Enabled),
		nullString(job.Metadata),
		nullTime(job.LastRunAt),
		nullTime(job.NextRunAt),
		job.RunCount,
		nullString(job.LastError),
		storage.FormatTime(job.CreatedAt),
		storage.FormatTime(job.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("create cron job: %w", err)
	}
	return nil
}

func (r *CronJobRepository) update(ctx context.Context, job *storage.CronJob, resetDelivery bool) error {
	query := `
UPDATE cron_jobs
SET handler = ?, schedule = ?, enabled = ?, metadata = ?, next_run_at = ?, updated_at = ?`
	args := []any{
		job.Handler,
		job.Schedule,
		boolInt(job.Enabled),
		nullString(job.Metadata),
		nullTime(job.NextRunAt),
		storage.FormatTime(job.UpdatedAt),
	}
	if resetDelivery {
		query += ", delivery_state = NULL, delivery_token = NULL"
	}
	query += "\nWHERE id = ?"
	args = append(args, job.ID)
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update cron job: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func scanCronJob(row interface{ Scan(dest ...any) error }) (*storage.CronJob, error) {
	var job storage.CronJob
	var enabled int
	var metadata, deliveryState, deliveryToken, lastRunAt, nextRunAt, lastError sql.NullString
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
		&lastError,
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
	job.LastError = lastError.String

	parsedCreatedAt, err := storage.ParseTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse cron job created_at: %w", err)
	}
	job.CreatedAt = parsedCreatedAt
	parsedUpdatedAt, err := storage.ParseTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse cron job updated_at: %w", err)
	}
	job.UpdatedAt = parsedUpdatedAt
	job.LastRunAt, err = storage.ParseOptionalTime(lastRunAt.String)
	if err != nil {
		return nil, fmt.Errorf("parse cron job last_run_at: %w", err)
	}
	job.NextRunAt, err = storage.ParseOptionalTime(nextRunAt.String)
	if err != nil {
		return nil, fmt.Errorf("parse cron job next_run_at: %w", err)
	}
	return &job, nil
}
