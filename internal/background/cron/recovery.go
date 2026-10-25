package cron

import (
	"context"
	background "elbot/internal/background/share"
	"elbot/internal/contextinfo"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	globalevents "elbot/internal/events"
	"elbot/internal/storage"
)

func (s *Service) RunMissedOnce(ctx context.Context) {
	for _, platformName := range s.connectedPlatformNames() {
		s.runMissedOnceForPlatform(ctx, platformName)
	}
}

func (s *Service) handlePlatformConnected(ctx context.Context, platformName string) {
	platformName = strings.TrimSpace(platformName)
	s.mu.Lock()
	s.connectedPlatforms[platformName] = true
	s.mu.Unlock()
	{
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelInfo,
			EventName:    "cron_platform_connected",
			Module:       "cron",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("cron platform connected\nplatform=%v", platformName),
			ContentLimit: 4096,
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		if facts.SessionID != "" {
			record.Identity.SessionID = &facts.SessionID
		}
		if facts.RunID != "" {
			record.Identity.RunID = &facts.RunID
		}
		if facts.Attempt != "" {
			record.Identity.Attempt = &facts.Attempt
		}
		if facts.RequestID != "" {
			record.Identity.RequestID = &facts.RequestID
		}
		if facts.RootRequestID != "" {
			record.Identity.RootRequestID = &facts.RootRequestID
		}
		background.LogBackground(ctx, record)
	}
	s.runMissedOnceForPlatform(ctx, platformName)
}

func (s *Service) runMissedOnceForPlatform(ctx context.Context, platformName string) {
	identity := globalevents.LogIdentity{}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if facts.SessionID != "" {
		identity.SessionID = &facts.SessionID
	}
	if facts.RunID != "" {
		identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		identity.RootRequestID = &facts.RootRequestID
	}

	jobs, err := s.store.CronJobs().ListEnabled(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled {
			return
		}
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "list_cron_jobs_for_missed_once_failed",
				Module:       "cron",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("list cron jobs for missed once failed\nplatform=%v\nerror=%v", platformName, err),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
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
			background.LogBackground(ctx, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelError, EventName: "missed_cron_run_failed", Module: "cron", Type: globalevents.ContentText, Content: fmt.Sprintf("job=%s\nstage=metadata\nerror=%v", job.Name, err), Identity: identity, ResultStatus: globalevents.ResultFailed})
			continue
		}
		if meta.Schedule.Mode != ScheduleOnce {
			continue
		}
		runAt, err := parseRunAt(meta.Schedule.RunAt)
		if err != nil {
			background.LogBackground(ctx, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelError, EventName: "missed_cron_run_failed", Module: "cron", Type: globalevents.ContentText, Content: fmt.Sprintf("job=%s\nstage=schedule\nerror=%v", job.Name, err), Identity: identity, ResultStatus: globalevents.ResultFailed})
			continue
		}
		if runAt.After(s.now()) || !containsString(s.targetPlatformNames(meta), platformName) {
			continue
		}
		unlock, lockErr := s.lockDeliveryJob(ctx, job.Name)
		if lockErr != nil {
			return
		}
		latest, loadErr := s.store.CronJobs().GetByName(ctx, job.Name)
		if loadErr != nil {
			unlock()
			if errors.Is(loadErr, context.Canceled) && ctx.Err() == context.Canceled {
				return
			}
			if !errors.Is(loadErr, storage.ErrNotFound) {
				background.LogBackground(ctx, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelError, EventName: "missed_cron_run_failed", Module: "cron", Type: globalevents.ContentText, Content: fmt.Sprintf("job=%s\nstage=reload\nerror=%v", job.Name, loadErr), Identity: identity, ResultStatus: globalevents.ResultFailed})
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
			background.LogBackground(ctx, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelError, EventName: "missed_cron_run_failed", Module: "cron", Type: globalevents.ContentText, Content: fmt.Sprintf("job=%s\nstage=reload_metadata\nerror=%v", job.Name, metaErr), Identity: identity, ResultStatus: globalevents.ResultFailed})
			continue
		}
		if latestMeta.Schedule.Mode != ScheduleOnce {
			unlock()
			continue
		}
		latestRunAt, runAtErr := parseRunAt(latestMeta.Schedule.RunAt)
		if runAtErr != nil {
			unlock()
			background.LogBackground(ctx, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelError, EventName: "missed_cron_run_failed", Module: "cron", Type: globalevents.ContentText, Content: fmt.Sprintf("job=%s\nstage=reload_schedule\nerror=%v", job.Name, runAtErr), Identity: identity, ResultStatus: globalevents.ResultFailed})
			continue
		}
		if latestRunAt.After(s.now()) || !containsString(s.targetPlatformNames(latestMeta), platformName) {
			unlock()
			continue
		}
		{
			content, _ := json.Marshal(map[string]any{"job": job.Name, "source_platform": meta.Target.SourcePlatform, "target_platforms": strings.Join(s.targetPlatformNames(meta), ","), "target_all_enabled_platforms": meta.Target.AllEnabledPlatforms, "platform": platformName})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelInfo,
				EventName:    "cron.missed_delivery_started",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "cron_missed_delivery_started",
				Module:       "cron",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("cron missed delivery started\njob=%v\nsource_platform=%v\ntarget_platforms=%v\ntarget_all_enabled_platforms=%v\nplatform=%v", job.Name, meta.Target.SourcePlatform, strings.Join(s.targetPlatformNames(meta), ","), meta.Target.AllEnabledPlatforms, platformName),
				ContentLimit: 4096,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
		logResult, deliverErr := s.deliverMissedOnce(ctx, *latest, latestMeta, platformName)
		resultStatus := logResult.ResultStatus
		unlock()
		if deliverErr != nil {
			{
				content, _ := json.Marshal(map[string]any{"job": job.Name, "source_platform": latestMeta.Target.SourcePlatform, "target_platforms": strings.Join(s.targetPlatformNames(latestMeta), ","), "target_all_enabled_platforms": latestMeta.Target.AllEnabledPlatforms, "platform": platformName, "error": deliverErr.Error()})
				record := globalevents.LogRecord{
					Category:     globalevents.LogAudit,
					Level:        slog.LevelError,
					EventName:    "cron.missed_delivery_failed",
					Module:       "cron",
					Type:         globalevents.ContentJSON,
					Content:      string(content),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				record.ResultStatus = resultStatus
				if resultStatus == globalevents.ResultCanceled {
					record.Level = slog.LevelInfo
				}

				record.Identity = identity
				background.LogBackground(ctx, record)
			}
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelError,
					EventName:    "missed_cron_run_failed",
					Module:       "cron",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("missed cron run failed\njob=%s\nrecovery_platform=%s\nsource_platform=%s\ntarget_platforms=%s\ntarget_all_enabled_platforms=%t", job.Name, platformName, latestMeta.Target.SourcePlatform, strings.Join(s.targetPlatformNames(latestMeta), ","), latestMeta.Target.AllEnabledPlatforms),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				record.ResultStatus = resultStatus
				for _, line := range strings.Split(logResult.Content, "\n") {
					if strings.TrimSpace(line) != "" {
						record.Content += "\n" + line
					}
				}
				if resultStatus == globalevents.ResultCanceled {
					record.Level = slog.LevelInfo
				}

				record.Identity = identity
				background.LogBackground(ctx, record)
			}
			if ctx.Err() != nil {
				return
			}
			// The sender owns failures of this asynchronous notice.
			_, _ = s.sendToPlatforms(ctx, job.Name, []string{"cli"}, fmt.Sprintf("cron 补跑失败：%s\n错误：%v", job.Name, deliverErr))
			continue
		}
		{
			content, _ := json.Marshal(map[string]any{"job": job.Name, "source_platform": latestMeta.Target.SourcePlatform, "target_platforms": strings.Join(s.targetPlatformNames(latestMeta), ","), "target_all_enabled_platforms": latestMeta.Target.AllEnabledPlatforms, "platform": platformName})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelInfo,
				EventName:    "cron.missed_delivery_completed",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSucceeded,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "cron_missed_delivery_completed",
				Module:       "cron",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("cron missed delivery completed\njob=%s\nrecovery_platform=%s\nsource_platform=%s\ntarget_platforms=%s\ntarget_all_enabled_platforms=%t", job.Name, platformName, latestMeta.Target.SourcePlatform, strings.Join(s.targetPlatformNames(latestMeta), ","), latestMeta.Target.AllEnabledPlatforms),
				ContentLimit: 4096,
				ResultStatus: resultStatus,
			}
			for _, line := range strings.Split(logResult.Content, "\n") {
				if strings.TrimSpace(line) != "" {
					record.Content += "\n" + line
				}
			}
			if resultStatus == globalevents.ResultFailed {
				record.Level = slog.LevelError
				record.EventName = "missed_cron_run_failed"
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
	}
}

func (s *Service) deliverMissedOnce(ctx context.Context, job storage.CronJob, meta Metadata, platformName string) (globalevents.LogResult, error) {
	content := "operation=deliverMissedOnce"
	job, state, logResult, prepareErr := s.prepareDelivery(ctx, job, meta)
	resultStatus := logResult.ResultStatus
	content += "\n" + logResult.Content
	if !state.ReportReady {
		{
			err := prepareErr
			logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
			return logResult, err
		}
	}
	logResult, deliverErr := s.deliverPrepared(ctx, job, meta, state, platformName, true)
	status := logResult.ResultStatus
	content += "\n" + logResult.Content
	if resultStatus == globalevents.ResultFailed || status == globalevents.ResultFailed {
		status = globalevents.ResultFailed
	} else if resultStatus == globalevents.ResultCanceled || status == globalevents.ResultCanceled {
		status = globalevents.ResultCanceled
	}
	{
		err := errors.Join(prepareErr, deliverErr)
		logResult := globalevents.LogResult{ResultStatus: status, Content: content}
		return logResult, err
	}
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
