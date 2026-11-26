package cron

import (
	"context"
	"elbot/internal/crondef"
	"elbot/internal/operationresult"
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

// missedOnceRun 将锁内实际执行的结果交给锁外的日志与通知。
type missedOnceRun struct {
	ctx       context.Context
	meta      crondef.Metadata
	state     CronDeliveryState
	delivered operationresult.Result[struct{}]
	err       error
}

func (s *Service) runMissedOnceForPlatform(ctx context.Context, platformName string) {
	jobs, err := s.store.ListEnabled(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled {
			return
		}
		globalevents.Log(ctx, globalevents.LogRecord{
			Category: globalevents.LogCategoryLog, Level: slog.LevelError, Module: "cron",
			EventName: "list_cron_jobs_for_missed_once_failed", Type: globalevents.ContentText,
			Content:      fmt.Sprintf("list cron jobs for missed once failed\nplatform=%v\nerror=%v", platformName, err),
			ContentLimit: 4096, Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.ResultFailed,
		})
		return
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		if job.Handler != crondef.UserHandlerName || s.missedOnceCandidate(ctx, job, platformName, "") == nil {
			continue
		}
		run := s.runMissedOnceJob(ctx, job.Name, platformName)
		if run == nil {
			continue
		}
		identity := globalevents.LogIdentityFromContext(run.ctx)
		target := run.meta.Target
		platforms := strings.Join(s.targetPlatformNames(run.meta), ",")
		details := fmt.Sprintf("job=%s\nrecovery_platform=%s\nsource_platform=%s\ntarget_platforms=%s\ntarget_all_enabled_platforms=%t", job.Name, platformName, target.SourcePlatform, platforms, target.AllEnabledPlatforms)
		deliveryFailed := run.err != nil || run.delivered.Status != operationresult.Success
		taskFailed := run.state.ReportReady && !run.state.TakenOver && !run.state.TaskCompleted
		if deliveryFailed || taskFailed {
			status, level, message := globalevents.ResultFailed, slog.LevelWarn, run.state.Report
			if run.err != nil {
				message = run.err.Error()
				status = globalevents.LogStatus(run.err)
				if status == globalevents.ResultCanceled {
					level = slog.LevelInfo
				}
			} else if deliveryFailed {
				message = run.delivered.Message
			}
			globalevents.Log(run.ctx, globalevents.LogRecord{
				Category: globalevents.LogCategoryLog, Level: level, Module: "cron",
				EventName: "missed_cron_run_failed", Type: globalevents.ContentText,
				Content:      "missed cron run failed\n" + details + "\nerror=" + message,
				ContentLimit: 4096, Identity: identity, ResultStatus: status,
			})
			if ctx.Err() != nil {
				return
			}
			if deliveryFailed && (!run.state.ReportReady || run.state.NeedReport) {
				notice := globalevents.NewNotification(run.ctx, slog.LevelWarn, message)
				notice.Target = globalevents.NotificationTarget{Platform: s.cliPlatformName}
				globalevents.Notify(run.ctx, notice)
			}
			continue
		}
		content, _ := json.Marshal(map[string]any{"job": job.Name, "source_platform": target.SourcePlatform, "target_platforms": platforms, "target_all_enabled_platforms": target.AllEnabledPlatforms, "platform": platformName})
		globalevents.Log(run.ctx, globalevents.LogRecord{
			Category: globalevents.LogCategoryAudit, Level: slog.LevelInfo, Module: "cron",
			EventName: "cron.missed_delivery_completed", Type: globalevents.ContentJSON, Content: string(content),
			ContentLimit: 4096, Identity: identity, ResultStatus: globalevents.ResultSucceeded,
		})
		globalevents.Log(run.ctx, globalevents.LogRecord{
			Category: globalevents.LogCategoryLog, Level: slog.LevelInfo, Module: "cron",
			EventName: "cron_missed_delivery_completed", Type: globalevents.ContentText, Content: "cron missed delivery completed\n" + details,
			ContentLimit: 4096, Identity: identity, ResultStatus: globalevents.ResultSucceeded,
		})
	}
}

func (s *Service) runMissedOnceJob(ctx context.Context, name, platformName string) *missedOnceRun {
	unlock, err := s.lockDeliveryJob(ctx, name)
	if err != nil {
		return nil
	}
	defer unlock()
	latest, err := s.store.GetByName(ctx, name)
	if err != nil {
		if !errors.Is(err, ErrJobNotFound) && !(errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled) {
			logMissedOnceError(ctx, name, "reload", err)
		}
		return nil
	}
	if !latest.Enabled {
		return nil
	}
	meta := s.missedOnceCandidate(ctx, *latest, platformName, "reload_")
	if meta == nil {
		return nil
	}
	target := meta.Target
	platforms := strings.Join(s.targetPlatformNames(*meta), ",")
	identity := globalevents.LogIdentityFromContext(ctx)
	content, _ := json.Marshal(map[string]any{"job": name, "source_platform": target.SourcePlatform, "target_platforms": platforms, "target_all_enabled_platforms": target.AllEnabledPlatforms, "platform": platformName})
	globalevents.Log(ctx, globalevents.LogRecord{
		Category: globalevents.LogCategoryAudit, Level: slog.LevelInfo, Module: "cron",
		EventName: "cron.missed_delivery_started", Type: globalevents.ContentJSON, Content: string(content),
		ContentLimit: 4096, Identity: identity,
	})
	globalevents.Log(ctx, globalevents.LogRecord{
		Category: globalevents.LogCategoryLog, Level: slog.LevelInfo, Module: "cron",
		EventName: "cron_missed_delivery_started", Type: globalevents.ContentText,
		Content:      fmt.Sprintf("cron missed delivery started\njob=%v\nsource_platform=%v\ntarget_platforms=%v\ntarget_all_enabled_platforms=%v\nplatform=%v", name, target.SourcePlatform, platforms, target.AllEnabledPlatforms, platformName),
		ContentLimit: 4096, Identity: identity,
	})
	run := &missedOnceRun{meta: *meta}
	run.ctx, run.state, run.delivered, run.err = s.deliverMissedOnce(ctx, *latest, *meta, platformName)
	return run
}

func (s *Service) missedOnceCandidate(ctx context.Context, job Job, platformName, stagePrefix string) *crondef.Metadata {
	meta, err := decodeMetadata(job.Metadata)
	if err != nil {
		logMissedOnceError(ctx, job.Name, stagePrefix+"metadata", err)
		return nil
	}
	if meta.Schedule.Mode != crondef.ScheduleOnce {
		return nil
	}
	runAt, err := parseRunAt(meta.Schedule.RunAt)
	if err != nil {
		logMissedOnceError(ctx, job.Name, stagePrefix+"schedule", err)
		return nil
	}
	if runAt.After(s.now()) || !containsString(s.targetPlatformNames(meta), platformName) {
		return nil
	}
	return &meta
}

func logMissedOnceError(ctx context.Context, name, stage string, err error) {
	globalevents.Log(ctx, globalevents.LogRecord{
		Category: globalevents.LogCategoryLog, Level: slog.LevelError, Module: "cron",
		EventName: "missed_cron_run_failed", Type: globalevents.ContentText,
		Content:  fmt.Sprintf("job=%s\nstage=%s\nerror=%v", name, stage, err),
		Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.ResultFailed,
	})
}

func (s *Service) deliverMissedOnce(ctx context.Context, job Job, meta crondef.Metadata, platformName string) (context.Context, CronDeliveryState, operationresult.Result[struct{}], error) {
	ctx, job, prepared, prepareErr := s.prepareDelivery(ctx, job, meta)
	if prepareErr != nil {
		return ctx, CronDeliveryState{}, operationresult.Result[struct{}]{}, prepareErr
	}
	if prepared.Status != operationresult.Success {
		return ctx, CronDeliveryState{}, operationresult.Result[struct{}]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	state := prepared.Data
	if !state.ReportReady {
		return ctx, state, operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}
	delivered, err := s.deliverPrepared(ctx, job, meta, state, platformName, true)
	return ctx, state, delivered, err
}

func missedOnceReportText(title, report string) string {
	prefix := strings.TrimSpace(title) + "补发："
	report = strings.TrimSpace(report)
	if report == "" {
		return prefix
	}
	return prefix + "\n\n" + report
}
