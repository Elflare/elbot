package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/elyph"
	globalevents "elbot/internal/events"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
)

const (
	UserHandlerName = "builtin.cron"
	metadataKind    = "llm_cron"
	timeLayout      = "2006-01-02 15:04:05"
)

type TargetSenderFunc func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error)

type PlatformTarget struct {
	Name          string
	SuperadminIDs []string
}

type LLMRunner interface {
	background.Runner
}

type Models interface {
	ResolveMode(string) modelmgr.Selection
	ValidateSelection(config.ModelSelection) error
}

type Service struct {
	manager *Manager
	store   storage.Store

	sendTarget       TargetSenderFunc
	runner           LLMRunner
	models           Models
	sessions         *session.Service
	enabledPlatforms []PlatformTarget
	sandboxRoot      string
	now              func() time.Time

	mu                 sync.Mutex
	connectedPlatforms map[string]bool
	deliveryGates      map[string]chan struct{}
	platformEvents     platformSubscriptions
}

type Options struct {
	Manager *Manager
	Store   storage.Store

	SendTarget       TargetSenderFunc
	Runner           LLMRunner
	Models           Models
	Sessions         *session.Service
	EnabledPlatforms []PlatformTarget
	SandboxRoot      string
}

func NewService(opts Options) *Service {
	sandboxRoot := strings.TrimSpace(opts.SandboxRoot)
	if sandboxRoot == "" {
		sandboxRoot = filepath.Join("data", "sandbox")
	}
	s := &Service{
		manager:            opts.Manager,
		store:              opts.Store,
		sendTarget:         opts.SendTarget,
		runner:             opts.Runner,
		sandboxRoot:        sandboxRoot,
		now:                time.Now,
		connectedPlatforms: map[string]bool{},
		deliveryGates:      map[string]chan struct{}{},
	}
	s.enabledPlatforms = normalizePlatformTargets(opts.EnabledPlatforms)
	s.models = opts.Models
	s.sessions = opts.Sessions
	return s
}

func (s *Service) SetRunner(runner LLMRunner) { s.runner = runner }

func (s *Service) Handler(ctx context.Context, job storage.CronJob) (logResult globalevents.LogResult, err error) {
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

	logResult.ResultStatus = globalevents.ResultFailed
	logResult.Content = "stage=lock"
	unlock, err := s.lockDeliveryJob(ctx, job.Name)
	if err != nil {
		if err == context.Canceled {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return globalevents.LogResult{ResultStatus: logResult.ResultStatus, Content: logResult.Content + fmt.Sprintf("\nerror=%q", err.Error())}, err
	}
	defer unlock()
	logResult.Content = "stage=load"
	latest, err := s.store.CronJobs().GetByName(ctx, job.Name)
	if err != nil {
		if err == context.Canceled {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return globalevents.LogResult{ResultStatus: logResult.ResultStatus, Content: logResult.Content + fmt.Sprintf("\nerror=%q", err.Error())}, err
	}
	if !latest.Enabled {
		return globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "reason=disabled"}, nil
	}
	job = *latest
	logResult.Content = "stage=metadata"
	meta, err := decodeMetadata(job.Metadata)
	if err != nil {
		if err == context.Canceled {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return globalevents.LogResult{ResultStatus: logResult.ResultStatus, Content: logResult.Content + fmt.Sprintf("\nerror=%q", err.Error())}, err
	}
	{
		content, _ := json.Marshal(map[string]any{"job": job.Name, "source_platform": meta.Target.SourcePlatform, "target_platforms": strings.Join(s.targetPlatformNames(meta), ","), "target_all_enabled_platforms": meta.Target.AllEnabledPlatforms, "trigger_mode": meta.Trigger.Mode, "schedule_mode": meta.Schedule.Mode})
		record := globalevents.LogRecord{
			Category:     globalevents.LogAudit,
			Level:        slog.LevelInfo,
			EventName:    "cron.trigger_started",
			Module:       "cron",
			Type:         globalevents.ContentJSON,
			Content:      string(content),
			ContentLimit: 4096,
		}
		record.Identity = identity
		background.LogBackground(ctx, record)
	}
	logResult.Content = fmt.Sprintf("source_platform=%s target_platforms=%s target_all_enabled_platforms=%t trigger_mode=%s schedule_mode=%s", meta.Target.SourcePlatform, strings.Join(s.targetPlatformNames(meta), ","), meta.Target.AllEnabledPlatforms, meta.Trigger.Mode, meta.Schedule.Mode)
	content := logResult.Content
	logResult.Content = ""
	var runErr error
	switch meta.Trigger.Mode {
	case TriggerDirect:
		logResult, runErr = s.runDirect(ctx, job, meta)
	case TriggerLLM:
		logResult, runErr = s.runLLM(ctx, job, meta)
	default:
		runErr = fmt.Errorf("unsupported trigger mode %q", meta.Trigger.Mode)
		logResult.Content = runErr.Error()
	}
	logResult.Content = content + "\n" + logResult.Content
	if logResult.ResultStatus == globalevents.ResultFailed || logResult.ResultStatus == globalevents.ResultCanceled {
		{
			content, _ := json.Marshal(map[string]any{"job": job.Name, "source_platform": meta.Target.SourcePlatform, "target_platforms": strings.Join(s.targetPlatformNames(meta), ","), "target_all_enabled_platforms": meta.Target.AllEnabledPlatforms, "result": fmt.Sprintf("%.2048s", logResult.Content)})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelError,
				EventName:    "cron.trigger_failed",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
				ResultStatus: logResult.ResultStatus,
			}
			if logResult.ResultStatus == globalevents.ResultCanceled {
				record.Level = slog.LevelInfo
			}

			record.Identity = identity
			background.LogBackground(ctx, record)
		}
		return globalevents.LogResult{ResultStatus: logResult.ResultStatus, Content: logResult.Content}, runErr
	}
	{
		content, _ := json.Marshal(map[string]any{"job": job.Name, "source_platform": meta.Target.SourcePlatform, "target_platforms": strings.Join(s.targetPlatformNames(meta), ","), "target_all_enabled_platforms": meta.Target.AllEnabledPlatforms, "result": fmt.Sprintf("%.2048s", logResult.Content)})
		record := globalevents.LogRecord{
			Category:     globalevents.LogAudit,
			Level:        slog.LevelInfo,
			EventName:    "cron.trigger_completed",
			Module:       "cron",
			Type:         globalevents.ContentJSON,
			Content:      string(content),
			ContentLimit: 4096,
			ResultStatus: logResult.ResultStatus,
		}
		record.Identity = identity
		background.LogBackground(ctx, record)
	}
	return globalevents.LogResult{ResultStatus: logResult.ResultStatus, Content: logResult.Content}, runErr
}

func (s *Service) Create(ctx context.Context, req UpsertRequest) (*storage.CronJob, error) {
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

	if err := requireSuperadmin(req.Actor); err != nil {
		{
			content, _ := json.Marshal(map[string]any{"operation": "create", "actor_id": req.Actor.ID, "reason": err.Error()})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelWarn,
				EventName:    "cron.permission_denied",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultRejected,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
		return nil, err
	}
	meta := Metadata{Kind: metadataKind, Version: 1, Title: strings.TrimSpace(req.Title), CreatedBy: actorMetadata(req.Actor), Schedule: CronSchedule{Mode: req.ScheduleMode, RunAt: strings.TrimSpace(req.RunAt), CronExpr: strings.TrimSpace(req.CronExpr)}, Trigger: CronTrigger{Mode: req.TriggerMode, Message: strings.TrimSpace(req.Message)}, Target: CronTarget{AllEnabledPlatforms: req.AllEnabledPlatforms, SourcePlatform: firstNonEmpty(req.SourcePlatform, req.Actor.Platform)}, LLM: CronLLMMetadata{ToolListNames: normalizeToolListNames(req.ToolListNames), ModelProvider: strings.TrimSpace(req.ModelProvider), Model: strings.TrimSpace(req.Model)}}

	if err := validateElyphCronTask(meta); err != nil {
		return nil, err
	}
	if req.Enabled {
		if err := s.validateUserSchedule(&meta); err != nil {
			return nil, err
		}
	}
	job, err := s.upsert(ctx, req.Name, meta, req.Enabled, true)
	if err != nil {
		return nil, err
	}
	{
		content, _ := json.Marshal(map[string]any{"job": job.Name, "actor_id": req.Actor.ID, "trigger_mode": meta.Trigger.Mode, "schedule_mode": meta.Schedule.Mode})
		record := globalevents.LogRecord{
			Category:     globalevents.LogAudit,
			Level:        slog.LevelInfo,
			EventName:    "cron.create",
			Module:       "cron",
			Type:         globalevents.ContentJSON,
			Content:      string(content),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultSucceeded,
		}
		record.Identity = identity
		background.LogBackground(ctx, record)
	}
	return job, nil
}

func (s *Service) Update(ctx context.Context, req PatchRequest) (*storage.CronJob, error) {
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

	if err := requireSuperadmin(req.Actor); err != nil {
		{
			content, _ := json.Marshal(map[string]any{"operation": "update", "actor_id": req.Actor.ID, "reason": err.Error()})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelWarn,
				EventName:    "cron.permission_denied",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultRejected,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
		return nil, err
	}
	name := normalizeJobName(req.Name)
	job, err := s.store.CronJobs().GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	meta, err := decodeMetadata(job.Metadata)
	if err != nil {
		return nil, err
	}
	previousMeta := meta
	if req.Title != nil {
		meta.Title = strings.TrimSpace(*req.Title)
	}
	if req.ScheduleMode != nil {
		meta.Schedule.Mode = *req.ScheduleMode
	}
	if req.RunAt != nil {
		meta.Schedule.RunAt = strings.TrimSpace(*req.RunAt)
	}
	if req.CronExpr != nil {
		meta.Schedule.CronExpr = strings.TrimSpace(*req.CronExpr)
	}
	if req.TriggerMode != nil {
		meta.Trigger.Mode = *req.TriggerMode
	}
	if req.Message != nil {
		meta.Trigger.Message = strings.TrimSpace(*req.Message)
	}
	if req.ToolListNames != nil {
		meta.LLM.ToolListNames = normalizeToolListNames(*req.ToolListNames)
	}
	if req.ModelProvider != nil || req.Model != nil {
		if req.ModelProvider == nil || req.Model == nil {
			return nil, fmt.Errorf("model_provider and model must be updated together")
		}
		meta.LLM.ModelProvider, meta.LLM.Model = strings.TrimSpace(*req.ModelProvider), strings.TrimSpace(*req.Model)
	}
	if req.AllEnabledPlatforms != nil {

		meta.Target.AllEnabledPlatforms = *req.AllEnabledPlatforms
	}
	enabled := job.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if err := validateElyphCronTask(meta); err != nil {
		return nil, err
	}
	if enabled {
		if err := s.validateUserSchedule(&meta); err != nil {
			return nil, err
		}
	}
	resetDelivery := startsNewDeliveryCycle(previousMeta, meta, req, enabled)
	updated, err := s.upsert(ctx, name, meta, enabled, resetDelivery)
	if err != nil {
		return nil, err
	}
	{
		content, _ := json.Marshal(map[string]any{"job": updated.Name, "actor_id": req.Actor.ID})
		record := globalevents.LogRecord{
			Category:     globalevents.LogAudit,
			Level:        slog.LevelInfo,
			EventName:    "cron.update",
			Module:       "cron",
			Type:         globalevents.ContentJSON,
			Content:      string(content),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultSucceeded,
		}
		record.Identity = identity
		background.LogBackground(ctx, record)
	}
	return updated, nil
}

func (s *Service) Disable(ctx context.Context, name string, actor contextinfo.Actor) error {
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

	if err := requireSuperadmin(actor); err != nil {
		{
			content, _ := json.Marshal(map[string]any{"operation": "disable", "actor_id": actor.ID, "reason": err.Error()})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelWarn,
				EventName:    "cron.permission_denied",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultRejected,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
		return err
	}
	name = normalizeJobName(name)
	if s.manager != nil {
		if err := s.manager.DisableJob(ctx, name); err != nil {
			return err
		}
	} else if err := s.store.CronJobs().DisableByName(ctx, name); err != nil {
		return err
	}
	{
		content, _ := json.Marshal(map[string]any{"job": name, "actor_id": actor.ID})
		record := globalevents.LogRecord{
			Category:     globalevents.LogAudit,
			Level:        slog.LevelInfo,
			EventName:    "cron.disable",
			Module:       "cron",
			Type:         globalevents.ContentJSON,
			Content:      string(content),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultSucceeded,
		}
		record.Identity = identity
		background.LogBackground(ctx, record)
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, name string, actor contextinfo.Actor) error {
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

	if err := requireSuperadmin(actor); err != nil {
		{
			content, _ := json.Marshal(map[string]any{"operation": "delete", "actor_id": actor.ID, "reason": err.Error()})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelWarn,
				EventName:    "cron.permission_denied",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultRejected,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
		return err
	}
	name = normalizeJobName(name)
	if s.manager != nil {
		if err := s.manager.DeleteJob(ctx, name); err != nil {
			return err
		}
	} else if err := s.store.CronJobs().DeleteByName(ctx, name); err != nil {
		return err
	}
	{
		content, _ := json.Marshal(map[string]any{"job": name, "actor_id": actor.ID})
		record := globalevents.LogRecord{
			Category:     globalevents.LogAudit,
			Level:        slog.LevelInfo,
			EventName:    "cron.delete",
			Module:       "cron",
			Type:         globalevents.ContentJSON,
			Content:      string(content),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultSucceeded,
		}
		record.Identity = identity
		background.LogBackground(ctx, record)
	}
	return nil
}

func (s *Service) Get(ctx context.Context, name string, actor contextinfo.Actor) (JobView, error) {
	if err := requireSuperadmin(actor); err != nil {
		{
			content, _ := json.Marshal(map[string]any{"operation": "get", "actor_id": actor.ID, "reason": err.Error()})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelWarn,
				EventName:    "cron.permission_denied",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultRejected,
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
		return JobView{}, err
	}
	job, err := s.store.CronJobs().GetByName(ctx, normalizeJobName(name))
	if err != nil {
		return JobView{}, err
	}
	meta, err := decodeMetadata(job.Metadata)
	if err != nil {
		return JobView{}, err
	}
	deliveryState, err := decodeDeliveryState(job.DeliveryState)
	if err != nil {
		return JobView{}, err
	}
	return JobView{Job: *job, Metadata: meta, Delivery: deliveryState}, nil
}

func (s *Service) List(ctx context.Context, includeDisabled, includeCompleted bool, actor contextinfo.Actor) ([]JobView, error) {
	if err := requireSuperadmin(actor); err != nil {
		{
			content, _ := json.Marshal(map[string]any{"operation": "list", "actor_id": actor.ID, "reason": err.Error()})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelWarn,
				EventName:    "cron.permission_denied",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultRejected,
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
		return nil, err
	}
	jobs, err := s.store.CronJobs().List(ctx, includeDisabled)
	if err != nil {
		return nil, err
	}
	views := []JobView{}
	for _, job := range jobs {
		meta, err := decodeMetadata(job.Metadata)
		if err != nil || meta.Kind != metadataKind {
			continue
		}
		deliveryState, stateErr := decodeDeliveryState(job.DeliveryState)
		if stateErr != nil {
			continue
		}
		if !includeCompleted && s.isCompletedCron(job, meta, deliveryState) {
			continue
		}
		views = append(views, JobView{Job: job, Metadata: meta, Delivery: deliveryState})
	}
	return views, nil
}

func validateElyphCronTask(meta Metadata) error {
	// LLM cron 要求 message 是合法 ELyph #task。
	// 执行时会注入规则卡，任务正文保持可 lint、可复用、可审计；
	// task 名仅作为任务内部标识，不要求和 cron job 名一致。
	if meta.Trigger.Mode != TriggerLLM {
		return nil
	}
	_, err := elyph.ParseTask(meta.Trigger.Message, "")
	return err
}

func (s *Service) validateUserSchedule(meta *Metadata) error {
	// once cron 是分钟级调度：过去时间直接拒绝。
	// 如果 run_at 落在当前分钟，中央 cron 本轮 tick 可能已经错过，
	// 这里顺延到下一分钟，避免任务看似创建成功但不会准时触发。
	if meta == nil || meta.Schedule.Mode != ScheduleOnce {
		return nil
	}
	runAt, err := parseRunAt(meta.Schedule.RunAt)
	if err != nil {
		return err
	}
	now := s.now()
	nowMinute := now.Truncate(time.Minute)
	runMinute := runAt.Truncate(time.Minute)
	if runMinute.Equal(nowMinute) {
		meta.Schedule.RunAt = nowMinute.Add(time.Minute).Format(timeLayout)
		return nil
	}
	if runMinute.Before(nowMinute) {
		return fmt.Errorf("run_at 时间不正确：一次性 cron 是分钟级调度，必须晚于当前这一分钟。当前时间：%s", now.Format(timeLayout))
	}
	return nil
}

func (s *Service) isCompletedCron(job storage.CronJob, meta Metadata, state CronDeliveryState) bool {
	if meta.Schedule.Mode != ScheduleOnce {
		return false
	}
	return !job.Enabled || (state.ReportReady && s.deliveryComplete(meta, state))
}

func startsNewDeliveryCycle(before, after Metadata, req PatchRequest, enabled bool) bool {
	if !enabled {
		return false
	}
	if req.Enabled != nil && *req.Enabled {
		return true
	}
	if before.Schedule != after.Schedule || before.Trigger != after.Trigger || before.Target != after.Target {
		return true
	}
	if before.LLM.ModelProvider != after.LLM.ModelProvider || before.LLM.Model != after.LLM.Model || strings.Join(before.LLM.ToolListNames, "\x00") != strings.Join(after.LLM.ToolListNames, "\x00") {
		return true
	}
	return false
}

func (s *Service) upsert(ctx context.Context, name string, meta Metadata, enabled, resetDelivery bool) (*storage.CronJob, error) {
	if err := s.validateModel(meta.LLM); err != nil {
		return nil, err
	}
	name = normalizeJobName(name)
	if name == "" {
		return nil, fmt.Errorf("cron name is required")
	}
	if meta.Title == "" {
		meta.Title = strings.TrimPrefix(name, "user.cron.")
	}
	if meta.Kind == "" {
		meta.Kind = metadataKind
	}
	if meta.Version < 2 {
		meta.Version = 2
	}
	schedule, err := scheduleExpr(meta.Schedule)
	if err != nil {
		return nil, err
	}
	if err := validateMetadata(meta); err != nil {
		return nil, err
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	if s.manager != nil {
		return s.manager.UpsertJob(ctx, UpsertJobRequest{Name: name, Handler: UserHandlerName, Schedule: schedule, Enabled: enabled, Metadata: string(data), ResetDelivery: resetDelivery})
	}
	nextRun, err := computeNextRunAt(schedule, string(data), enabled, s.now())
	if err != nil {
		return nil, err
	}
	return s.store.CronJobs().Upsert(ctx, storage.UpsertCronJobRequest{Name: name, Handler: UserHandlerName, Schedule: schedule, Enabled: enabled, Metadata: string(data), NextRunAt: nextRun, ResetDelivery: resetDelivery})
}

func normalizePlatformTargets(targets []PlatformTarget) []PlatformTarget {
	seen := map[string]bool{}
	out := []PlatformTarget{{Name: "cli", SuperadminIDs: []string{"local"}}}
	seen["cli"] = true
	for _, target := range targets {
		name := strings.TrimSpace(target.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, PlatformTarget{Name: name, SuperadminIDs: trimStrings(target.SuperadminIDs)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Service) enabledPlatformNames() []string {
	out := make([]string, 0, len(s.enabledPlatforms))
	for _, target := range s.enabledPlatforms {
		out = append(out, target.Name)
	}
	return out
}

func targetOwnerID(target PlatformTarget) string {
	ids := trimStrings(target.SuperadminIDs)
	if len(ids) == 0 {
		return ""
	}
	return security.ActorID(target.Name, ids[0])
}

func trimStrings(values []string) []string {
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Service) targetPlatformNames(meta Metadata) []string {
	if meta.Target.AllEnabledPlatforms {
		return s.enabledPlatformNames()
	}
	return uniqueStrings([]string{meta.Target.SourcePlatform})
}

func containsString(values []string, value string) bool {
	value = strings.TrimSpace(value)
	for _, existing := range values {
		if strings.TrimSpace(existing) == value {
			return true
		}
	}
	return false
}
