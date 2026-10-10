package cron

import (
	"context"
	"elbot/internal/background"
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
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "cron_legacy_delivery_migrated",
				Module:       "cron",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("cron legacy delivery migrated\njob=%v\nsource_platform=%v\ntarget_platforms=%v\ntarget_all_enabled_platforms=%v", job.Name, meta.Target.SourcePlatform, strings.Join(s.targetPlatformNames(meta), ","), meta.Target.AllEnabledPlatforms),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSucceeded,
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
		if s.deliveryComplete(meta, state) {
			if _, err := s.disableCompletedDelivery(ctx, job.Name, state.RunID); err != nil {
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

func (s *Service) handlePlatformConnected(ctx context.Context, platformName string) {
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
		if ctx.Err() != nil {
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
					Content:      fmt.Sprintf("missed cron run failed\njob=%v\nplatform=%v\nerror=%v", job.Name, platformName, deliverErr),
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
			if ctx.Err() != nil {
				return
			}
			if ctx.Err() == nil {
				_, _ = s.sendToPlatforms(ctx, job.Name, []string{"cli"}, fmt.Sprintf("cron 补跑失败：%s\n错误：%v", job.Name, deliverErr))
			}
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
				Content:      fmt.Sprintf("cron missed delivery completed\njob=%v\nsource_platform=%v\ntarget_platforms=%v\ntarget_all_enabled_platforms=%v\nplatform=%v", job.Name, latestMeta.Target.SourcePlatform, strings.Join(s.targetPlatformNames(latestMeta), ","), latestMeta.Target.AllEnabledPlatforms, platformName),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSucceeded,
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
			if err != nil {
				logResult.Content += fmt.Sprintf("\nerror=%v", err)
			}
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
		if err != nil {
			logResult.Content += fmt.Sprintf("\nerror=%v", err)
		}
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
