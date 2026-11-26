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

func (r *Store) UpsertCronJob(ctx context.Context, req UpsertCronJobRequest) (*CronJob, error) {
	now := time.Now()
	job, err := r.GetByName(ctx, req.Name)
	if errors.Is(err, share.ErrNotFound) {
		job = &CronJob{
			ID:        share.NewID(),
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
	return r.update(ctx, job, req.ResetDelivery)
}

func cronJobMatchesUpsert(job *CronJob, req UpsertCronJobRequest) bool {
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
	if err != nil {
		return fmt.Errorf("update cron job next run: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("disable cron job rows: %w", err)
	}
	if n == 0 {
		return share.ErrNotFound
	}
	return nil
}

func (r *Store) UpdateRunState(ctx context.Context, id string, state CronJobRunState) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE cron_jobs
SET last_run_at = ?, run_count = run_count + 1, updated_at = ?
WHERE id = ?`,
		share.FormatTime(state.LastRunAt),
		share.FormatTime(state.UpdatedAt),
		id,
	)
	if err != nil {
		return fmt.Errorf("update cron job run state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete cron job rows: %w", err)
	}
	if n == 0 {
		return share.ErrNotFound
	}
	return nil
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
	if err != nil {
		return fmt.Errorf("disable cron job: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return share.ErrNotFound
	}
	return nil
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
	if err != nil {
		return fmt.Errorf("delete cron job: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return share.ErrNotFound
	}
	return nil
}

func (r *Store) create(ctx context.Context, job *CronJob) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO cron_jobs (
    id, name, handler, schedule, enabled, metadata, last_run_at, next_run_at,
    run_count, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID,
		job.Name,
		job.Handler,
		job.Schedule,
		job.Enabled,
		share.NullString(job.Metadata),
		nullTime(job.LastRunAt),
		nullTime(job.NextRunAt),
		job.RunCount,
		share.FormatTime(job.CreatedAt),
		share.FormatTime(job.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("create cron job: %w", err)
	}
	return nil
}

func (r *Store) update(ctx context.Context, job *CronJob, resetDelivery bool) (*CronJob, error) {
	query := `
UPDATE cron_jobs
SET handler = ?, schedule = ?, enabled = ?, metadata = ?, next_run_at = ?, updated_at = ?`
	args := []any{
		job.Handler,
		job.Schedule,
		job.Enabled,
		share.NullString(job.Metadata),
		nullTime(job.NextRunAt),
		share.FormatTime(job.UpdatedAt),
	}
	if resetDelivery {
		query += ", delivery_state = NULL, delivery_token = NULL"
	}
	query += ` WHERE id = ? RETURNING ` + cronJobColumns
	args = append(args, job.ID)
	job, err := scanCronJob(r.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, share.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update cron job: %w", err)
	}
	return job, nil
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
