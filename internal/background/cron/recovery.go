package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	globalevents "elbot/internal/events"
)

func (s *Service) handlePlatformConnected(ctx context.Context, platformName string) {
	platformName = strings.TrimSpace(platformName)
	{
		record := globalevents.LogRecord{
			Category:     globalevents.LogCategoryLog,
			Level:        slog.LevelInfo,
			EventName:    "cron_platform_connected",
			Module:       "cron",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("cron platform connected\nplatform=%v", platformName),
			ContentLimit: 4096,
		}
		record.Identity = globalevents.LogIdentityFromContext(ctx)
		globalevents.Log(ctx, record)
	}
	s.runMissedOnceForPlatform(ctx, platformName)
}

func (s *Service) runMissedOnceForPlatform(ctx context.Context, platformName string) {
	identity := globalevents.LogIdentityFromContext(ctx)

	jobs, err := s.store.ListEnabled(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled {
			return
		}
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogCategoryLog,
				Level:        slog.LevelError,
				EventName:    "list_cron_jobs_for_missed_once_failed",
				Module:       "cron",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("list cron jobs for missed once failed\nplatform=%v\nerror=%v", platformName, err),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			record.Identity = identity
			globalevents.Log(ctx, record)
		}
		return
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		if job.Handler != UserHandlerName {
			continue
		}
		meta, err := decodeMetadata(job.Metadata)
		if err != nil {
			globalevents.Log(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: slog.LevelError, EventName: "missed_cron_run_failed", Module: "cron", Type: globalevents.ContentText, Content: fmt.Sprintf("job=%s\nstage=metadata\nerror=%v", job.Name, err), Identity: identity, ResultStatus: globalevents.ResultFailed})
			continue
		}
		if meta.Schedule.Mode != ScheduleOnce {
			continue
		}
		runAt, err := parseRunAt(meta.Schedule.RunAt)
		if err != nil {
			globalevents.Log(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: slog.LevelError, EventName: "missed_cron_run_failed", Module: "cron", Type: globalevents.ContentText, Content: fmt.Sprintf("job=%s\nstage=schedule\nerror=%v", job.Name, err), Identity: identity, ResultStatus: globalevents.ResultFailed})
			continue
		}
		if runAt.After(s.now()) || !containsString(s.targetPlatformNames(meta), platformName) {
			continue
		}
		unlock, lockErr := s.lockDeliveryJob(ctx, job.Name)
		if lockErr != nil {
			return
		}
		latest, loadErr := s.store.GetByName(ctx, job.Name)
		if loadErr != nil {
			unlock()
			if errors.Is(loadErr, context.Canceled) && ctx.Err() == context.Canceled {
				return
			}
			if !errors.Is(loadErr, ErrJobNotFound) {
				globalevents.Log(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: slog.LevelError, EventName: "missed_cron_run_failed", Module: "cron", Type: globalevents.ContentText, Content: fmt.Sprintf("job=%s\nstage=reload\nerror=%v", job.Name, loadErr), Identity: identity, ResultStatus: globalevents.ResultFailed})
			}
			continue
		}
		if !latest.Enabled {
			unlock()
			continue
		}
		latestMeta, metaErr := decodeMetadata(latest.Metadata)
		if metaErr != nil {
			unlock()
			globalevents.Log(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: slog.LevelError, EventName: "missed_cron_run_failed", Module: "cron", Type: globalevents.ContentText, Content: fmt.Sprintf("job=%s\nstage=reload_metadata\nerror=%v", job.Name, metaErr), Identity: identity, ResultStatus: globalevents.ResultFailed})
			continue
		}
		if latestMeta.Schedule.Mode != ScheduleOnce {
			unlock()
			continue
		}
		latestRunAt, runAtErr := parseRunAt(latestMeta.Schedule.RunAt)
		if runAtErr != nil {
			unlock()
			globalevents.Log(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: slog.LevelError, EventName: "missed_cron_run_failed", Module: "cron", Type: globalevents.ContentText, Content: fmt.Sprintf("job=%s\nstage=reload_schedule\nerror=%v", job.Name, runAtErr), Identity: identity, ResultStatus: globalevents.ResultFailed})
			continue
		}
		if latestRunAt.After(s.now()) || !containsString(s.targetPlatformNames(latestMeta), platformName) {
			unlock()
			continue
		}
		{
			content, _ := json.Marshal(map[string]any{"job": job.Name, "source_platform": meta.Target.SourcePlatform, "target_platforms": strings.Join(s.targetPlatformNames(meta), ","), "target_all_enabled_platforms": meta.Target.AllEnabledPlatforms, "platform": platformName})
			record := globalevents.LogRecord{
				Category:     globalevents.LogCategoryAudit,
				Level:        slog.LevelInfo,
				EventName:    "cron.missed_delivery_started",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
			}
			record.Identity = identity
			globalevents.Log(ctx, record)
		}
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogCategoryLog,
				Level:        slog.LevelInfo,
				EventName:    "cron_missed_delivery_started",
				Module:       "cron",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("cron missed delivery started\njob=%v\nsource_platform=%v\ntarget_platforms=%v\ntarget_all_enabled_platforms=%v\nplatform=%v", job.Name, meta.Target.SourcePlatform, strings.Join(s.targetPlatformNames(meta), ","), meta.Target.AllEnabledPlatforms, platformName),
				ContentLimit: 4096,
			}
			record.Identity = identity
			globalevents.Log(ctx, record)
		}
		requestCtx, state, deliverErr := s.deliverMissedOnce(ctx, *latest, latestMeta, platformName)
		identity = globalevents.LogIdentityFromContext(requestCtx)
		resultStatus := globalevents.ResultSucceeded
		if deliverErr != nil {
			resultStatus = globalevents.ResultFailed
			if globalevents.LogStatus(deliverErr) == globalevents.ResultCanceled {
				resultStatus = globalevents.ResultCanceled
			}
		}
		unlock()
		if deliverErr != nil {
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogCategoryLog,
					Level:        slog.LevelWarn,
					EventName:    "missed_cron_run_failed",
					Module:       "cron",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("missed cron run failed\njob=%s\nrecovery_platform=%s\nsource_platform=%s\ntarget_platforms=%s\ntarget_all_enabled_platforms=%t", job.Name, platformName, latestMeta.Target.SourcePlatform, strings.Join(s.targetPlatformNames(latestMeta), ","), latestMeta.Target.AllEnabledPlatforms),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				record.ResultStatus = resultStatus
				record.Content += "\nerror=" + deliverErr.Error()

				if resultStatus == globalevents.ResultCanceled {
					record.Level = slog.LevelInfo
				}

				record.Identity = identity
				globalevents.Log(requestCtx, record)
			}
			if ctx.Err() != nil {
				return
			}
			if !state.ReportReady || state.NeedReport {
				notice := globalevents.NewNotification(requestCtx, slog.LevelWarn, deliverErr.Error())
				notice.Target = globalevents.NotificationTarget{Platform: s.cliPlatformName}
				globalevents.Notify(requestCtx, notice)
			}
			continue
		}
		{
			content, _ := json.Marshal(map[string]any{"job": job.Name, "source_platform": latestMeta.Target.SourcePlatform, "target_platforms": strings.Join(s.targetPlatformNames(latestMeta), ","), "target_all_enabled_platforms": latestMeta.Target.AllEnabledPlatforms, "platform": platformName})
			record := globalevents.LogRecord{
				Category:     globalevents.LogCategoryAudit,
				Level:        slog.LevelInfo,
				EventName:    "cron.missed_delivery_completed",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSucceeded,
			}
			record.Identity = identity
			globalevents.Log(ctx, record)
		}
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogCategoryLog,
				Level:        slog.LevelInfo,
				EventName:    "cron_missed_delivery_completed",
				Module:       "cron",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("cron missed delivery completed\njob=%s\nrecovery_platform=%s\nsource_platform=%s\ntarget_platforms=%s\ntarget_all_enabled_platforms=%t", job.Name, platformName, latestMeta.Target.SourcePlatform, strings.Join(s.targetPlatformNames(latestMeta), ","), latestMeta.Target.AllEnabledPlatforms),
				ContentLimit: 4096,
				ResultStatus: resultStatus,
			}

			record.Identity = identity
			globalevents.Log(ctx, record)
		}
	}
}

func (s *Service) deliverMissedOnce(ctx context.Context, job Job, meta Metadata, platformName string) (context.Context, CronDeliveryState, error) {
	ctx, job, state, prepareErr := s.prepareDelivery(ctx, job, meta)
	if prepareErr != nil || !state.ReportReady {
		return ctx, state, prepareErr
	}
	return ctx, state, s.deliverPrepared(ctx, job, meta, state, platformName, true)
}

func missedOnceReportText(title, report string) string {
	prefix := strings.TrimSpace(title) + "补发："
	report = strings.TrimSpace(report)
	if report == "" {
		return prefix
	}
	return prefix + "\n\n" + report
}
