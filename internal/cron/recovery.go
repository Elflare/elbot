package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	globalevents "elbot/internal/events"
	"elbot/internal/storage"
)

// MigrateLegacyDeliveryState moves completed or pending once-delivery data out
// of metadata before the scheduler can mistake an old task for a fresh run.
func (s *Service) MigrateLegacyDeliveryState(ctx context.Context) error {
	jobs, err := s.store.CronJobs().ListEnabled(ctx)
	if err != nil {
		return fmt.Errorf("list cron jobs for legacy delivery migration: %w", err)
	}
	for _, job := range jobs {
		if job.Handler != UserHandlerName || job.RunCount == 0 || job.NextRunAt != nil || job.DeliveryState != "" || job.DeliveryToken != "" {
			continue
		}
		meta, err := decodeMetadata(job.Metadata)
		if err != nil || meta.Schedule.Mode != ScheduleOnce {
			continue
		}
		legacy, err := decodeLegacyDeliveryMetadata(job.Metadata)
		if err != nil || !hasLegacyDeliveryMetadata(legacy) {
			continue
		}

		state := s.legacyDeliveryState(meta, legacy)
		encoded, err := encodeDeliveryState(state)
		if err != nil {
			return fmt.Errorf("encode legacy cron delivery %s: %w", job.Name, err)
		}
		swapped, err := s.store.CronJobs().CompareAndSwapDelivery(ctx, job.ID, "", state.RunID, encoded)
		if err != nil {
			return fmt.Errorf("migrate legacy cron delivery %s: %w", job.Name, err)
		}
		if !swapped {
			continue
		}
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogRuntime,
			Level:    slog.LevelInfo,
			Name:     "cron_legacy_delivery_migrated",
			Module:   "cron",
			Summary:  "cron legacy delivery migrated",
			Fields:   slog.Group("", s.cronLogAttrs(job.Name, meta)...).Value.Group(),
		})
		if s.deliveryComplete(meta, state) {
			if err := s.disableCompletedDelivery(ctx, job.Name, state.RunID); err != nil {
				return fmt.Errorf("disable migrated cron delivery %s: %w", job.Name, err)
			}
		}
	}
	return nil
}

func (s *Service) legacyDeliveryState(meta Metadata, legacy legacyCronDeliveryMetadata) CronDeliveryState {
	state := CronDeliveryState{
		RunID:           storage.NewID(),
		ReportReady:     true,
		TaskCompleted:   legacy.Completed,
		Report:          legacy.Report,
		ReportSegments:  legacy.ReportSegments,
		ReportSessionID: legacy.ReportSessionID,
		ReportMessageID: legacy.ReportMessageID,
	}
	outputIDs := deliveryOutputIDs(state)
	for _, target := range s.resolveDeliveryTargets(meta, "") {
		if !containsString(legacy.DeliveredPlatforms, target.platform) {
			continue
		}
		targetState := ensureDeliveryTargetState(&state, target.key)
		for _, outputID := range outputIDs {
			outputState := ensureDeliveryOutputState(targetState, outputID)
			outputState.Status = DeliveryDelivered
		}
	}
	return state
}

func (s *Service) RunMissedOnce(ctx context.Context) {
	for _, platformName := range s.connectedPlatformNames() {
		s.runMissedOnceForPlatform(ctx, platformName)
	}
}

func (s *Service) NotifyPlatformConnected(ctx context.Context, platformName string) {
	platformName = strings.TrimSpace(platformName)
	if platformName == "" {
		return
	}
	s.mu.Lock()
	if s.connectedPlatforms == nil {
		s.connectedPlatforms = map[string]bool{}
	}
	s.connectedPlatforms[platformName] = true
	s.mu.Unlock()
	_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
		Category: globalevents.LogRuntime,
		Level:    slog.LevelInfo,
		Name:     "cron_platform_connected",
		Module:   "cron",
		Summary:  "cron platform connected",
		Fields:   []slog.Attr{slog.Any("platform", platformName)},
	})
	s.runMissedOnceForPlatform(ctx, platformName)
}

func (s *Service) runMissedOnceForPlatform(ctx context.Context, platformName string) {
	jobs, err := s.store.CronJobs().ListEnabled(ctx)
	if err != nil {
		if isContextCancellation(ctx, err) {
			return
		}
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogRuntime,
			Level:    slog.LevelError,
			Name:     "list_cron_jobs_for_missed_once_failed",
			Module:   "cron",
			Summary:  "list cron jobs for missed once failed",
			Fields:   []slog.Attr{slog.Any("platform", platformName), slog.Any("error", err)},
		})
		return
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		meta, err := decodeMetadata(job.Metadata)
		if err != nil || meta.Kind != metadataKind || meta.Schedule.Mode != ScheduleOnce {
			continue
		}
		runAt, err := parseRunAt(meta.Schedule.RunAt)
		if err != nil || runAt.After(s.now()) || !containsString(s.targetPlatformNames(meta), platformName) {
			continue
		}
		unlock, lockErr := s.lockDeliveryJob(ctx, job.Name)
		if lockErr != nil {
			return
		}
		latest, loadErr := s.store.CronJobs().GetByName(ctx, job.Name)
		if loadErr != nil || !latest.Enabled {
			unlock()
			continue
		}
		latestMeta, metaErr := decodeMetadata(latest.Metadata)
		latestRunAt, runAtErr := parseRunAt(latestMeta.Schedule.RunAt)
		if metaErr != nil || latestMeta.Kind != metadataKind || latestMeta.Schedule.Mode != ScheduleOnce || runAtErr != nil || latestRunAt.After(s.now()) || !containsString(s.targetPlatformNames(latestMeta), platformName) {
			unlock()
			continue
		}
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogAudit,
			Level:    slog.LevelInfo,
			Name:     "cron.missed_delivery_started",
			Module:   "cron",
			Summary:  "cron.missed_delivery_started",
			Fields:   slog.Group("", s.cronAuditAttrs(job.Name, meta, "platform", platformName)...).Value.Group(),
		})
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogRuntime,
			Level:    slog.LevelInfo,
			Name:     "cron_missed_delivery_started",
			Module:   "cron",
			Summary:  "cron missed delivery started",
			Fields:   slog.Group("", s.cronLogAttrs(job.Name, meta, "platform", platformName)...).Value.Group(),
		})
		deliverErr := s.deliverMissedOnce(ctx, *latest, latestMeta, platformName)
		unlock()
		if deliverErr != nil {
			if isContextCancellation(ctx, deliverErr) {
				return
			}
			_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
				Category: globalevents.LogAudit,
				Level:    slog.LevelWarn,
				Name:     "cron.missed_delivery_failed",
				Module:   "cron",
				Summary:  "cron.missed_delivery_failed",
				Fields:   slog.Group("", s.cronAuditAttrs(job.Name, latestMeta, "platform", platformName, "error", deliverErr.Error())...).Value.Group(),
			})
			_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
				Category: globalevents.LogRuntime,
				Level:    slog.LevelWarn,
				Name:     "missed_cron_run_failed",
				Module:   "cron",
				Summary:  "missed cron run failed",
				Fields:   []slog.Attr{slog.Any("job", job.Name), slog.Any("platform", platformName), slog.Any("error", deliverErr)},
			})
			if ctx.Err() == nil {
				_ = s.sendToPlatforms(ctx, job.Name, []string{"cli"}, fmt.Sprintf("cron 补跑失败：%s\n错误：%v", job.Name, deliverErr))
			}
			continue
		}
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogAudit,
			Level:    slog.LevelInfo,
			Name:     "cron.missed_delivery_completed",
			Module:   "cron",
			Summary:  "cron.missed_delivery_completed",
			Fields:   slog.Group("", s.cronAuditAttrs(job.Name, latestMeta, "platform", platformName)...).Value.Group(),
		})
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogRuntime,
			Level:    slog.LevelInfo,
			Name:     "cron_missed_delivery_completed",
			Module:   "cron",
			Summary:  "cron missed delivery completed",
			Fields:   slog.Group("", s.cronLogAttrs(job.Name, latestMeta, "platform", platformName)...).Value.Group(),
		})
	}
}

func (s *Service) deliverMissedOnce(ctx context.Context, job storage.CronJob, meta Metadata, platformName string) error {
	job, state, prepareErr := s.prepareDelivery(ctx, job, meta)
	if !state.ReportReady {
		return prepareErr
	}
	deliverErr := s.deliverPrepared(ctx, job, meta, state, platformName, true)
	return errors.Join(prepareErr, deliverErr)
}

// isContextCancellation excludes mixed errors containing an actual failure.
func isContextCancellation(ctx context.Context, err error) bool {
	if ctx.Err() == nil || err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if child != nil && !isContextCancellation(ctx, child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok && wrapped.Unwrap() != nil {
		return isContextCancellation(ctx, wrapped.Unwrap())
	}
	return errors.Is(err, ctx.Err())
}

func missedOnceReportText(title, report string) string {
	prefix := strings.TrimSpace(title) + "补发："
	report = strings.TrimSpace(report)
	if report == "" {
		return prefix
	}
	return prefix + "\n\n" + report
}

func (s *Service) connectedPlatformNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for platformName := range s.connectedPlatforms {
		out = append(out, platformName)
	}
	sort.Strings(out)
	return out
}
