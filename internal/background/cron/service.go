package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"elbot/internal/agent"
	background "elbot/internal/background/share"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
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

type Models interface {
	ResolveMode(string) modelmgr.Selection
	ValidateSelection(config.ModelSelection) error
}

type Service struct {
	manager *Manager
	store   JobRepository

	sendTarget       TargetSenderFunc
	runner           background.Runner
	models           Models
	media            background.ReportMedia
	copyBackground   func(context.Context, agent.BackgroundCopyRequest) (string, error)
	sessionTakenOver background.SessionTakenOver
	enabledPlatforms []PlatformTarget
	sandboxRoot      string
	now              func() time.Time

	mu             sync.Mutex
	deliveryGates  map[string]*deliveryGate
	platformEvents platformSubscriptions
}

type Options struct {
	Manager *Manager
	Store   JobRepository

	SendTarget       TargetSenderFunc
	Runner           background.Runner
	Models           Models
	Media            background.ReportMedia
	CopyBackground   func(context.Context, agent.BackgroundCopyRequest) (string, error)
	SessionTakenOver background.SessionTakenOver
	EnabledPlatforms []PlatformTarget
	SandboxRoot      string
}

func NewService(opts Options) *Service {
	s := &Service{
		manager:       opts.Manager,
		store:         opts.Store,
		sendTarget:    opts.SendTarget,
		runner:        opts.Runner,
		sandboxRoot:   opts.SandboxRoot,
		now:           time.Now,
		media:         opts.Media,
		deliveryGates: map[string]*deliveryGate{},
	}
	s.enabledPlatforms = normalizePlatformTargets(opts.EnabledPlatforms)
	s.models = opts.Models
	s.copyBackground = opts.CopyBackground
	s.sessionTakenOver = opts.SessionTakenOver
	return s
}

func (s *Service) Handler(ctx context.Context, job Job) (logResult globalevents.LogResult, err error) {
	identity := cronLogIdentity(ctx)

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
	latest, err := s.store.GetByName(ctx, job.Name)
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
	case TriggerDirect, TriggerLLM:
		logResult, runErr = s.runTask(ctx, job, meta)
	default:
		runErr = fmt.Errorf("unsupported trigger mode %q", meta.Trigger.Mode)
		logResult.Content = runErr.Error()
	}
	logResult.Content = content + "\n" + logResult.Content
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
			Identity:     identity,
		}
		if logResult.ResultStatus == globalevents.ResultFailed || logResult.ResultStatus == globalevents.ResultCanceled {
			record.EventName = "cron.trigger_failed"
		}
		if logResult.ResultStatus == globalevents.ResultFailed {
			record.Level = slog.LevelError
		}
		background.LogBackground(ctx, record)
	}
	return logResult, runErr
}

func cronLogIdentity(ctx context.Context) globalevents.LogIdentity {
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
	return identity
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
	ids := target.SuperadminIDs
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
