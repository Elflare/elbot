package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"elbot/internal/background"
	"elbot/internal/elyph"
	"elbot/internal/output"
	"elbot/internal/security"
	"elbot/internal/storage"
)

const (
	UserHandlerName = "builtin.cron"
	metadataKind    = "llm_cron"
	timeLayout      = "2006-01-02 15:04:05"
)

type AuditFunc func(event string, attrs ...any)

type TargetSenderFunc func(ctx context.Context, target output.Target, out output.Output) error

type PlatformTarget struct {
	Name          string
	SuperadminIDs []string
}

type LLMRunner interface {
	background.Runner
}

type RunCronMessageRequest struct {
	JobName       string
	Title         string
	Platform      string
	Actor         security.Actor
	ScopeID       string
	SessionID     string
	ModelProvider string
	Model         string
	Prompt        string
	ToolListNames []string
}

type RunCronMessageResult struct {
	SessionID string
	Text      string
}

type Service struct {
	manager          *Manager
	store            storage.Store
	logger           *slog.Logger
	audit            AuditFunc
	sendTarget       TargetSenderFunc
	runner           LLMRunner
	enabledPlatforms []PlatformTarget
	now              func() time.Time

	mu                 sync.Mutex
	connectedPlatforms map[string]bool
}

type Options struct {
	Manager          *Manager
	Store            storage.Store
	Logger           *slog.Logger
	Audit            AuditFunc
	SendTarget       TargetSenderFunc
	Runner           LLMRunner
	EnabledPlatforms []PlatformTarget
}

type ScheduleMode string

const (
	ScheduleOnce ScheduleMode = "once"
	ScheduleCron ScheduleMode = "cron"
)

type TriggerMode string

const (
	TriggerDirect TriggerMode = "direct"
	TriggerLLM    TriggerMode = "llm"
)

type Metadata struct {
	Kind      string               `json:"kind"`
	Version   int                  `json:"version"`
	Title     string               `json:"title"`
	CreatedBy CronActor            `json:"created_by"`
	Schedule  CronSchedule         `json:"schedule"`
	Trigger   CronTrigger          `json:"trigger"`
	Target    CronTarget           `json:"target"`
	LLM       CronLLMMetadata      `json:"llm,omitempty"`
	Delivery  CronDeliveryMetadata `json:"delivery,omitempty"`
}

type CronActor struct {
	ActorID        string `json:"actor_id"`
	Platform       string `json:"platform"`
	PlatformUserID string `json:"platform_user_id"`
	DisplayName    string `json:"display_name,omitempty"`
}

type CronSchedule struct {
	Mode     ScheduleMode `json:"mode"`
	RunAt    string       `json:"run_at,omitempty"`
	CronExpr string       `json:"cron_expr,omitempty"`
}

type CronTrigger struct {
	Mode    TriggerMode `json:"mode"`
	Message string      `json:"message"`
}

type CronTarget struct {
	AllEnabledPlatforms bool   `json:"all_enabled_platforms"`
	SourcePlatform      string `json:"source_platform"`
}

type CronLLMMetadata struct {
	SessionID     string   `json:"session_id,omitempty"`
	ToolListNames []string `json:"tool_list_names,omitempty"`
}

type CronDeliveryMetadata struct {
	Completed          bool     `json:"completed,omitempty"`
	Report             string   `json:"report,omitempty"`
	DeliveredPlatforms []string `json:"delivered_platforms,omitempty"`
}

type UpsertRequest struct {
	Name                string
	Title               string
	ScheduleMode        ScheduleMode
	RunAt               string
	CronExpr            string
	TriggerMode         TriggerMode
	Message             string
	ToolListNames       []string
	AllEnabledPlatforms bool

	Enabled        bool
	Actor          security.Actor
	SourcePlatform string
}

type PatchRequest struct {
	Name                string
	Title               *string
	ScheduleMode        *ScheduleMode
	RunAt               *string
	CronExpr            *string
	TriggerMode         *TriggerMode
	Message             *string
	ToolListNames       *[]string
	AllEnabledPlatforms *bool

	Enabled *bool
	Actor   security.Actor
}

type JobView struct {
	Job      storage.CronJob `json:"job"`
	Metadata Metadata        `json:"metadata"`
}

type CronLLMResult = background.JSONResult

func NewService(opts Options) *Service {
	s := &Service{manager: opts.Manager, store: opts.Store, logger: opts.Logger, audit: opts.Audit, sendTarget: opts.SendTarget, runner: opts.Runner, now: time.Now, connectedPlatforms: map[string]bool{}}
	s.enabledPlatforms = normalizePlatformTargets(opts.EnabledPlatforms)
	return s
}

func (s *Service) SetRunner(runner LLMRunner) { s.runner = runner }

func (s *Service) Handler(ctx context.Context, job storage.CronJob) error {
	meta, err := decodeMetadata(job.Metadata)
	if err != nil {
		s.logWarn("cron metadata parse failed", "job", job.Name, "error", err)
		return err
	}
	s.auditEvent("cron.trigger_started", s.cronAuditAttrs(job.Name, meta, "trigger_mode", meta.Trigger.Mode, "schedule_mode", meta.Schedule.Mode)...)
	s.logInfo("cron trigger started", s.cronLogAttrs(job.Name, meta, "trigger_mode", meta.Trigger.Mode, "schedule_mode", meta.Schedule.Mode)...)
	var runErr error
	switch meta.Trigger.Mode {
	case TriggerDirect:
		runErr = s.runDirect(ctx, job, meta)
	case TriggerLLM:
		runErr = s.runLLM(ctx, job, meta)
	default:
		runErr = fmt.Errorf("unsupported trigger mode %q", meta.Trigger.Mode)
	}
	if meta.Schedule.Mode == ScheduleOnce && runErr == nil && s.manager != nil {
		if err := s.manager.DisableJob(ctx, job.Name); err != nil {
			s.logWarn("disable once cron after run failed", "job", job.Name, "error", err)
		}
	}
	if runErr != nil {
		s.auditEvent("cron.trigger_failed", s.cronAuditAttrs(job.Name, meta, "error", runErr.Error())...)
		s.logWarn("cron trigger failed", s.cronLogAttrs(job.Name, meta, "error", runErr.Error())...)
		return runErr
	}
	s.auditEvent("cron.trigger_completed", s.cronAuditAttrs(job.Name, meta)...)
	s.logInfo("cron trigger completed", s.cronLogAttrs(job.Name, meta)...)
	return nil
}

func (s *Service) Create(ctx context.Context, req UpsertRequest) (*storage.CronJob, error) {
	if err := requireSuperadmin(req.Actor); err != nil {
		s.auditEvent("cron.permission_denied", "operation", "create", "actor_id", req.Actor.ID, "reason", err.Error())
		return nil, err
	}
	meta := Metadata{Kind: metadataKind, Version: 1, Title: strings.TrimSpace(req.Title), CreatedBy: actorMetadata(req.Actor), Schedule: CronSchedule{Mode: req.ScheduleMode, RunAt: strings.TrimSpace(req.RunAt), CronExpr: strings.TrimSpace(req.CronExpr)}, Trigger: CronTrigger{Mode: req.TriggerMode, Message: strings.TrimSpace(req.Message)}, Target: CronTarget{AllEnabledPlatforms: req.AllEnabledPlatforms, SourcePlatform: firstNonEmpty(req.SourcePlatform, req.Actor.Platform)}, LLM: CronLLMMetadata{ToolListNames: normalizeToolListNames(req.ToolListNames)}}

	if err := validateElyphCronTask(meta); err != nil {
		return nil, err
	}
	if req.Enabled {
		if err := s.validateUserSchedule(&meta); err != nil {
			return nil, err
		}
	}
	job, err := s.upsert(ctx, req.Name, meta, req.Enabled)
	if err != nil {
		return nil, err
	}
	s.auditEvent("cron.create", "job", job.Name, "actor_id", req.Actor.ID, "trigger_mode", meta.Trigger.Mode, "schedule_mode", meta.Schedule.Mode)
	return job, nil
}

func (s *Service) Update(ctx context.Context, req PatchRequest) (*storage.CronJob, error) {
	if err := requireSuperadmin(req.Actor); err != nil {
		s.auditEvent("cron.permission_denied", "operation", "update", "actor_id", req.Actor.ID, "reason", err.Error())
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
	updated, err := s.upsert(ctx, name, meta, enabled)
	if err != nil {
		return nil, err
	}
	s.auditEvent("cron.update", "job", updated.Name, "actor_id", req.Actor.ID)
	return updated, nil
}

func (s *Service) Disable(ctx context.Context, name string, actor security.Actor) error {
	if err := requireSuperadmin(actor); err != nil {
		s.auditEvent("cron.permission_denied", "operation", "disable", "actor_id", actor.ID, "reason", err.Error())
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
	s.auditEvent("cron.disable", "job", name, "actor_id", actor.ID)
	return nil
}

func (s *Service) Delete(ctx context.Context, name string, actor security.Actor) error {
	if err := requireSuperadmin(actor); err != nil {
		s.auditEvent("cron.permission_denied", "operation", "delete", "actor_id", actor.ID, "reason", err.Error())
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
	s.auditEvent("cron.delete", "job", name, "actor_id", actor.ID)
	return nil
}

func (s *Service) Get(ctx context.Context, name string, actor security.Actor) (JobView, error) {
	if err := requireSuperadmin(actor); err != nil {
		s.auditEvent("cron.permission_denied", "operation", "get", "actor_id", actor.ID, "reason", err.Error())
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
	return JobView{Job: *job, Metadata: meta}, nil
}

func (s *Service) List(ctx context.Context, includeDisabled, includeCompleted bool, actor security.Actor) ([]JobView, error) {
	if err := requireSuperadmin(actor); err != nil {
		s.auditEvent("cron.permission_denied", "operation", "list", "actor_id", actor.ID, "reason", err.Error())
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
		if !includeCompleted && s.isCompletedCron(job, meta) {
			continue
		}
		views = append(views, JobView{Job: job, Metadata: meta})
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

func (s *Service) isCompletedCron(job storage.CronJob, meta Metadata) bool {
	if meta.Schedule.Mode != ScheduleOnce {
		return false
	}
	return job.RunCount > 0 || meta.Delivery.Completed || !job.Enabled
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
		// 平台重连或多平台恢复时，missed once 可能被重复扫描。
		// delivered_platforms 用于按平台去重，避免同一个 once 对同一平台重复投递。
		if err != nil || runAt.After(s.now()) || !containsString(s.targetPlatformNames(meta), platformName) || hasDeliveredPlatform(meta, platformName) {
			continue
		}
		s.auditEvent("cron.missed_delivery_started", s.cronAuditAttrs(job.Name, meta, "platform", platformName)...)
		s.logInfo("cron missed delivery started", s.cronLogAttrs(job.Name, meta, "platform", platformName)...)
		if err := s.deliverMissedOnce(ctx, job, meta, platformName); err != nil {
			s.auditEvent("cron.missed_delivery_failed", s.cronAuditAttrs(job.Name, meta, "platform", platformName, "error", err.Error())...)
			s.logWarn("missed cron run failed", "job", job.Name, "platform", platformName, "error", err)
			_ = s.sendToPlatforms(context.Background(), job.Name, []string{"cli"}, fmt.Sprintf("cron 补跑失败：%s\n错误：%v", job.Name, err))
			continue
		}
		latest, err := s.store.CronJobs().GetByName(ctx, job.Name)
		if err == nil {
			if latestMeta, metaErr := decodeMetadata(latest.Metadata); metaErr == nil {
				s.auditEvent("cron.missed_delivery_completed", s.cronAuditAttrs(job.Name, latestMeta, "platform", platformName, "delivered_platforms", strings.Join(latestMeta.Delivery.DeliveredPlatforms, ","))...)
				s.logInfo("cron missed delivery completed", s.cronLogAttrs(job.Name, latestMeta, "platform", platformName, "delivered_platforms", strings.Join(latestMeta.Delivery.DeliveredPlatforms, ","))...)
				if !latest.Enabled {
					s.auditEvent("cron.missed_delivery_all_completed", s.cronAuditAttrs(job.Name, latestMeta)...)
					s.logInfo("cron missed delivery all completed", s.cronLogAttrs(job.Name, latestMeta)...)
				}
			}
		}
	}
}

func (s *Service) deliverMissedOnce(ctx context.Context, job storage.CronJob, meta Metadata, platformName string) error {
	report := strings.TrimSpace(meta.Delivery.Report)
	if meta.Trigger.Mode == TriggerDirect {
		meta.Delivery.Completed = true
		report = strings.TrimSpace(meta.Trigger.Message)
		meta.Delivery.Report = report
	} else if meta.Trigger.Mode == TriggerLLM {
		generatedLLM := false
		if !meta.Delivery.Completed {
			updated, llmReport, err := s.runLLMReport(ctx, job, meta)
			meta = updated
			report = strings.TrimSpace(llmReport)
			meta.Delivery.Completed = true
			meta.Delivery.Report = report
			generatedLLM = true
			if _, persistErr := s.upsert(ctx, job.Name, meta, job.Enabled); persistErr != nil {
				s.logWarn("persist cron delivery report failed", "job", job.Name, "error", persistErr)
			}
			if err != nil {
				return err
			}
		}
		defer func() {
			if generatedLLM {
				for _, connected := range s.connectedPlatformNames() {
					if connected != platformName && containsString(s.targetPlatformNames(meta), connected) {
						s.runMissedOnceForPlatform(context.Background(), connected)
					}
				}
			}
		}()
	} else {
		return fmt.Errorf("unsupported trigger mode %q", meta.Trigger.Mode)
	}
	if report != "" {
		if err := s.sendToPlatforms(ctx, job.Name, []string{platformName}, report); err != nil {
			return err
		}
	}
	meta.Delivery.DeliveredPlatforms = addUniqueString(meta.Delivery.DeliveredPlatforms, platformName)
	if _, err := s.upsert(ctx, job.Name, meta, job.Enabled); err != nil {
		return err
	}
	if s.allDelivered(meta) {
		if s.manager != nil {
			if err := s.manager.DisableJob(ctx, job.Name); err != nil {
				return err
			}
		} else if err := s.store.CronJobs().DisableByName(ctx, job.Name); err != nil {
			return err
		}
		meta.Delivery.DeliveredPlatforms = uniqueStrings(meta.Delivery.DeliveredPlatforms)
		return nil
	}
	return nil
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

func (s *Service) upsert(ctx context.Context, name string, meta Metadata, enabled bool) (*storage.CronJob, error) {
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
	if meta.Version == 0 {
		meta.Version = 1
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
		return s.manager.UpsertJob(ctx, UpsertJobRequest{Name: name, Handler: UserHandlerName, Schedule: schedule, Enabled: enabled, Metadata: string(data)})
	}
	return s.store.CronJobs().Upsert(ctx, storage.UpsertCronJobRequest{Name: name, Handler: UserHandlerName, Schedule: schedule, Enabled: enabled, Metadata: string(data)})
}

func (s *Service) runDirect(ctx context.Context, job storage.CronJob, meta Metadata) error {
	report := strings.TrimSpace(meta.Trigger.Message)
	if err := s.sendToTargets(ctx, job.Name, meta, report); err != nil {
		return err
	}
	if meta.Schedule.Mode == ScheduleOnce {
		meta.Delivery.Completed = true
		meta.Delivery.Report = report
		meta.Delivery.DeliveredPlatforms = addUniqueStrings(meta.Delivery.DeliveredPlatforms, s.targetPlatformNames(meta))
		if _, err := s.upsert(ctx, job.Name, meta, job.Enabled); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) runLLM(ctx context.Context, job storage.CronJob, meta Metadata) error {
	updated, report, err := s.runLLMReport(ctx, job, meta)
	if _, persistErr := s.upsert(ctx, job.Name, updated, job.Enabled); persistErr != nil {
		s.logWarn("persist cron delivery report failed", "job", job.Name, "error", persistErr)
	}
	if err != nil {
		if report != "" {
			if sendErr := s.sendToTargets(ctx, job.Name, updated, report); sendErr != nil {
				return errors.Join(err, sendErr)
			}
		}
		return err
	}
	if report == "" {
		return nil
	}
	if err := s.sendToTargets(ctx, job.Name, updated, report); err != nil {
		return err
	}
	if updated.Schedule.Mode == ScheduleOnce {
		updated.Delivery.DeliveredPlatforms = addUniqueStrings(updated.Delivery.DeliveredPlatforms, s.targetPlatformNames(updated))
		if _, persistErr := s.upsert(ctx, job.Name, updated, job.Enabled); persistErr != nil {
			s.logWarn("persist cron delivery platforms failed", "job", job.Name, "error", persistErr)
		}
	}
	return nil
}

func (s *Service) runLLMReport(ctx context.Context, job storage.CronJob, meta Metadata) (Metadata, string, error) {
	reusable := meta.Schedule.Mode == ScheduleOnce
	if reusable && meta.Delivery.Completed {
		return meta, strings.TrimSpace(meta.Delivery.Report), nil
	}
	if s.runner == nil {
		return meta, "", fmt.Errorf("cron llm runner is not configured")
	}
	actor := security.Actor{ID: meta.CreatedBy.ActorID, Platform: meta.CreatedBy.Platform, PlatformUserID: meta.CreatedBy.PlatformUserID, DisplayName: meta.CreatedBy.DisplayName, Role: security.RoleSuperadmin}
	prompt := cronPrompt(meta.Trigger.Message)
	result, err := s.runner.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: job.Name, Title: meta.Title, Platform: meta.Target.SourcePlatform, Actor: actor, ScopeID: cronScopeID(job.Name), SessionID: meta.LLM.SessionID, Prompt: prompt, ToolListNames: meta.LLM.ToolListNames, SandboxSubdir: string(background.KindCron), Metadata: map[string]string{"cron_job_name": job.Name}})

	if err != nil {
		return meta, "", err
	}
	if result.SessionID != "" && result.SessionID != meta.LLM.SessionID {
		meta.LLM.SessionID = result.SessionID
	}
	parsed, err := parseLLMResult(result.Text)
	if err != nil {
		result, parsed, err = s.retryLLMResultFormat(ctx, job, meta, actor, result.SessionID)
		if result.SessionID != "" && result.SessionID != meta.LLM.SessionID {
			meta.LLM.SessionID = result.SessionID
		}
	}
	if err != nil {
		message := cronParseFailedMessage(meta.Title, result.SessionID, err)
		if reusable {
			meta.Delivery.Completed = true
			meta.Delivery.Report = message
		}
		return meta, message, err
	}
	if meta.Target.AllEnabledPlatforms {
		if err := s.copySessionToBroadcastTargets(ctx, result.SessionID, meta, job.Name); err != nil {
			s.logWarn("copy cron session failed", "job", job.Name, "error", err)
		}
	}
	if reusable {
		meta.Delivery.Completed = parsed.Completed
	}
	if parsed.Completed {
		report := strings.TrimSpace(parsed.Report)
		if reusable {
			meta.Delivery.Report = report
		}
		return meta, report, nil
	}
	report := strings.TrimSpace(parsed.Report)
	if report == "" {
		report = "任务未完成。"
	}
	report += fmt.Sprintf("\n可 /resume 到 cron session 查看详情。\nsession: %s", result.SessionID)
	if reusable {
		meta.Delivery.Report = report
	}
	return meta, report, nil
}

func (s *Service) retryLLMResultFormat(ctx context.Context, job storage.CronJob, meta Metadata, actor security.Actor, sessionID string) (background.RunResult, CronLLMResult, error) {
	result, err := s.runner.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: job.Name, Title: meta.Title, Platform: meta.Target.SourcePlatform, Actor: actor, ScopeID: cronScopeID(job.Name), SessionID: firstNonEmpty(sessionID, meta.LLM.SessionID), Prompt: cronFormatRetryPrompt(), SandboxSubdir: string(background.KindCron), Metadata: map[string]string{"cron_job_name": job.Name}})
	if err != nil {
		return result, CronLLMResult{}, err
	}
	if result.SessionID == "" {
		result.SessionID = firstNonEmpty(sessionID, meta.LLM.SessionID)
	}
	parsed, err := parseLLMResult(result.Text)
	return result, parsed, err
}

func cronParseFailedMessage(title, sessionID string, err error) string {
	return fmt.Sprintf("cron 任务 %s 解析格式失败，请 /resume 到 cron session 查看详情。\nsession: %s\n错误：%v", title, sessionID, err)
}

func (s *Service) sendToTargets(ctx context.Context, jobName string, meta Metadata, text string) error {
	if meta.Target.AllEnabledPlatforms {
		return s.sendToPlatforms(ctx, jobName, s.enabledPlatformNames(), text)
	}
	return s.sendToPlatforms(ctx, jobName, []string{meta.Target.SourcePlatform}, text)
}

func (s *Service) sendToPlatforms(ctx context.Context, jobName string, platforms []string, text string) error {
	if s.sendTarget == nil {
		return fmt.Errorf("cron target sender is not configured")
	}
	var errs []error
	for _, platformName := range uniqueStrings(platforms) {
		if platformName == "" {
			continue
		}
		attrs := []any{"job", jobName, "platform", platformName, "target", "superadmins"}
		s.auditEvent("cron.send_started", attrs...)
		s.logInfo("cron send started", attrs...)
		err := s.sendTarget(ctx, output.Target{Platform: platformName, Superadmins: true}, output.Text(text))
		if err != nil {
			err = fmt.Errorf("send %s: %w", platformName, err)
			errs = append(errs, err)
			s.auditEvent("cron.send_failed", "job", jobName, "platform", platformName, "target", "superadmins", "error", err.Error())
			s.logWarn("cron send failed", "job", jobName, "platform", platformName, "target", "superadmins", "error", err.Error())
			continue
		}
		s.auditEvent("cron.send_completed", attrs...)
		s.logInfo("cron send completed", attrs...)
	}
	return errors.Join(errs...)
}

func (s *Service) copySessionToBroadcastTargets(ctx context.Context, sourceSessionID string, meta Metadata, jobName string) error {
	if sourceSessionID == "" || s.store == nil || s.store.Sessions() == nil || s.store.Messages() == nil {
		return nil
	}
	source, err := s.store.Sessions().Get(ctx, sourceSessionID)
	if err != nil {
		return err
	}
	messages, err := s.store.Messages().ListBySession(ctx, sourceSessionID)
	if err != nil {
		return err
	}
	for _, target := range s.enabledPlatforms {
		if target.Name == "" || target.Name == source.Platform {
			continue
		}
		owner := targetOwnerID(target)
		if owner == "" {
			continue
		}
		copySession := &storage.Session{OwnerID: owner, Platform: target.Name, PlatformScopeID: cronScopeID(jobName), Mode: source.Mode, Title: meta.Title, Status: storage.SessionStatusActive, Metadata: cronSessionMetadata(jobName, sourceSessionID, true)}
		if err := s.store.Sessions().Create(ctx, copySession); err != nil {
			return err
		}
		for _, msg := range messages {
			msg.ID = ""
			msg.SessionID = copySession.ID
			msg.ParentMessageID = ""
			msg.ReplyToMessageID = ""
			msg.ReplyToPlatformMessageID = ""
			if err := s.store.Messages().Append(ctx, &msg); err != nil {
				return err
			}
		}
		s.auditEvent("cron.session_copied", "job", jobName, "source_session_id", sourceSessionID, "target_session_id", copySession.ID, "platform", target.Name)
	}
	return nil
}

func decodeMetadata(raw string) (Metadata, error) {
	var meta Metadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return Metadata{}, err
	}
	if meta.Kind != metadataKind {
		return Metadata{}, fmt.Errorf("unsupported cron metadata kind %q", meta.Kind)
	}
	return meta, nil
}

func validateMetadata(meta Metadata) error {
	if strings.TrimSpace(meta.Trigger.Message) == "" {
		return fmt.Errorf("message is required")
	}
	if meta.Trigger.Mode != TriggerDirect && meta.Trigger.Mode != TriggerLLM {
		return fmt.Errorf("unsupported trigger mode %q", meta.Trigger.Mode)
	}
	if meta.Target.SourcePlatform == "" {
		return fmt.Errorf("source platform is required")
	}
	_, err := scheduleExpr(meta.Schedule)
	return err
}

func scheduleExpr(schedule CronSchedule) (string, error) {
	switch schedule.Mode {
	case ScheduleOnce:
		runAt, err := parseRunAt(schedule.RunAt)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %d %d %d *", runAt.Minute(), runAt.Hour(), runAt.Day(), int(runAt.Month())), nil
	case ScheduleCron:
		expr := strings.TrimSpace(schedule.CronExpr)
		if expr == "" {
			return "", fmt.Errorf("cron_expr is required")
		}
		if len(strings.Fields(expr)) != 5 {
			return "", fmt.Errorf("cron_expr must be a 5-field cron expression")
		}
		return expr, nil
	default:
		return "", fmt.Errorf("unsupported schedule mode %q", schedule.Mode)
	}
}

func parseRunAt(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("run_at is required")
	}
	runAt, err := time.ParseInLocation(timeLayout, value, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("run_at must use YYYY-MM-DD HH:MM:SS: %w", err)
	}
	return runAt, nil
}

func normalizeJobName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "user.cron.")
	name = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`).ReplaceAllString(name, "_")
	name = strings.Trim(name, "._-")
	if name == "" {
		return ""
	}
	return "user.cron." + name
}

func cronScopeID(jobName string) string { return "cron:" + normalizeJobName(jobName) }

func cronSessionMetadata(jobName, sourceSessionID string, copied bool) string {
	data, _ := json.Marshal(map[string]any{"title_renamed": true, "title_source": "cron", "cron_job_name": jobName, "cron_source_session_id": sourceSessionID, "cron_broadcast_copy": copied})
	return string(data)
}

func actorMetadata(actor security.Actor) CronActor {
	return CronActor{ActorID: actor.ID, Platform: actor.Platform, PlatformUserID: actor.PlatformUserID, DisplayName: actor.DisplayName}
}

func requireSuperadmin(actor security.Actor) error {
	if actor.Role != security.RoleSuperadmin {
		return fmt.Errorf("cron requires superadmin role")
	}
	return nil
}

func normalizeToolListNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func cronPrompt(message string) string {

	return `[系统 Cron 后台任务]

` + elyph.RuleCard() + `

** “Cron 任务内容”必须是 ELyph #task <name> - 描述 任务文本
** 按“Cron 任务内容”中的 ELyph 任务自主执行
** 信息不足时，在最终 JSON 的 report 填写失败或阻塞原因
** 需要使用工具时直接使用工具
** 最终回复必须是严格 JSON
** JSON 格式：{"completed":true,"need_report":false,"report":""}
** completed 表示是否完成任务
** need_report 只有 completed=true 时有效
** report 为需要发给用户的汇报，未完成时填写失败或阻塞原因
~ 把任务当作前台用户对话
~ 闲聊
~ 向用户提问
~ 输出 Markdown 代码块
~ 输出 JSON 外的任何文字

Cron 任务内容：
` + strings.TrimSpace(message)
}

func cronFormatRetryPrompt() string {
	return background.DefaultJSONRetryPrompt()
}

func parseLLMResult(text string) (CronLLMResult, error) {
	return background.ParseJSONResult(text)
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

func (s *Service) logInfo(msg string, attrs ...any) {
	if s.logger != nil {
		s.logger.Info(msg, attrs...)
	}
}

func (s *Service) logWarn(msg string, attrs ...any) {
	if s.logger != nil {
		s.logger.Warn(msg, attrs...)
	}
}

func (s *Service) cronAuditAttrs(jobName string, meta Metadata, attrs ...any) []any {
	base := []any{"job", jobName, "source_platform", meta.Target.SourcePlatform, "target_platforms", strings.Join(s.targetPlatformNames(meta), ","), "target_all_enabled_platforms", meta.Target.AllEnabledPlatforms}
	return append(base, attrs...)
}

func (s *Service) cronLogAttrs(jobName string, meta Metadata, attrs ...any) []any {
	return s.cronAuditAttrs(jobName, meta, attrs...)
}

func (s *Service) targetPlatformNames(meta Metadata) []string {
	if meta.Target.AllEnabledPlatforms {
		return s.enabledPlatformNames()
	}
	return uniqueStrings([]string{meta.Target.SourcePlatform})
}

func (s *Service) allDelivered(meta Metadata) bool {
	if !meta.Delivery.Completed {
		return false
	}
	for _, platformName := range s.targetPlatformNames(meta) {
		if !hasDeliveredPlatform(meta, platformName) {
			return false
		}
	}
	return true
}

func hasDeliveredPlatform(meta Metadata, platformName string) bool {
	platformName = strings.TrimSpace(platformName)
	for _, delivered := range meta.Delivery.DeliveredPlatforms {
		if strings.TrimSpace(delivered) == platformName {
			return true
		}
	}
	return false
}

func addUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if strings.TrimSpace(existing) == value {
			return values
		}
	}
	return append(values, value)
}

func addUniqueStrings(values []string, additions []string) []string {
	for _, value := range additions {
		values = addUniqueString(values, value)
	}
	return values
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

func (s *Service) auditEvent(event string, attrs ...any) {
	if s.audit != nil {
		s.audit(event, attrs...)
	}
}
