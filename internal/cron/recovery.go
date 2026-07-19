package cron

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"elbot/internal/storage"
)

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
	s.logInfo("cron platform connected", "platform", platformName)
	s.runMissedOnceForPlatform(ctx, platformName)
}

func (s *Service) runMissedOnceForPlatform(ctx context.Context, platformName string) {
	jobs, err := s.store.CronJobs().ListEnabled(ctx)
	if err != nil {
		s.logWarn("list cron jobs for missed once failed", "platform", platformName, "error", err)
		return
	}
	for _, job := range jobs {
		meta, err := decodeMetadata(job.Metadata)
		if err != nil || meta.Kind != metadataKind || meta.Schedule.Mode != ScheduleOnce {
			continue
		}
		runAt, err := parseRunAt(meta.Schedule.RunAt)
		if err != nil || runAt.After(s.now()) || !containsString(s.targetPlatformNames(meta), platformName) {
			continue
		}
		unlock := s.lockDeliveryJob(job.Name)
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
		s.auditEvent("cron.missed_delivery_started", s.cronAuditAttrs(job.Name, meta, "platform", platformName)...)
		s.logInfo("cron missed delivery started", s.cronLogAttrs(job.Name, meta, "platform", platformName)...)
		deliverErr := s.deliverMissedOnce(ctx, *latest, latestMeta, platformName)
		unlock()
		if deliverErr != nil {
			s.auditEvent("cron.missed_delivery_failed", s.cronAuditAttrs(job.Name, latestMeta, "platform", platformName, "error", deliverErr.Error())...)
			s.logWarn("missed cron run failed", "job", job.Name, "platform", platformName, "error", deliverErr)
			_ = s.sendToPlatforms(context.Background(), job.Name, []string{"cli"}, fmt.Sprintf("cron 补跑失败：%s\n错误：%v", job.Name, deliverErr))
			continue
		}
		s.auditEvent("cron.missed_delivery_completed", s.cronAuditAttrs(job.Name, latestMeta, "platform", platformName)...)
		s.logInfo("cron missed delivery completed", s.cronLogAttrs(job.Name, latestMeta, "platform", platformName)...)
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
