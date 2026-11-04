package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
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
)

const (
	UserHandlerName = "builtin.cron"
	metadataKind    = "llm_cron"
	timeLayout      = "2006-01-02 15:04:05"
)

type TargetSenderFunc func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error)

type PlatformTarget struct {
	Name          string
	SuperadminIDs []string
}

type Models interface {
	ResolveMode(string) modelmgr.Selection
	ValidateSelection(config.ModelSelection) error
}

type Service struct {
	cliPlatformName string // 本地平台身份。
	newRunID        func(context.Context) (string, error)
	manager         *Manager
	store           JobRepository

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
	CLIPlatformName string                                // 根部注入的本地平台身份，用于默认目标和恢复通知。
	NewRunID        func(context.Context) (string, error) // 根部提供运行批次身份。
	Manager         *Manager
	Store           JobRepository

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
		cliPlatformName: opts.CLIPlatformName,
		newRunID:        opts.NewRunID,
		manager:         opts.Manager,
		store:           opts.Store,
		sendTarget:      opts.SendTarget,
		runner:          opts.Runner,
		sandboxRoot:     opts.SandboxRoot,
		now:             time.Now,
		media:           opts.Media,
		deliveryGates:   map[string]*deliveryGate{},
	}
	s.enabledPlatforms = normalizePlatformTargets(opts.CLIPlatformName, opts.EnabledPlatforms)
	s.models = opts.Models
	s.copyBackground = opts.CopyBackground
	s.sessionTakenOver = opts.SessionTakenOver
	return s
}

func (s *Service) Handler(ctx context.Context, job Job) (err error) {
	identity := cronLogIdentity(ctx)

	unlock, err := s.lockDeliveryJob(ctx, job.Name)
	if err != nil {
		return err
	}
	defer unlock()

	latest, err := s.store.GetByName(ctx, job.Name)
	if err != nil {
		return err
	}
	if !latest.Enabled {
		return nil
	}
	job = *latest

	meta, err := decodeMetadata(job.Metadata)
	if err != nil {
		return err
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

	var runErr error
	switch meta.Trigger.Mode {
	case TriggerDirect, TriggerLLM:
		runErr = s.runTask(ctx, job, meta)
	default:
		runErr = fmt.Errorf("unsupported trigger mode %q", meta.Trigger.Mode)

	}

	{
		content, _ := json.Marshal(map[string]any{"job": job.Name, "source_platform": meta.Target.SourcePlatform, "target_platforms": strings.Join(s.targetPlatformNames(meta), ","), "target_all_enabled_platforms": meta.Target.AllEnabledPlatforms, "result": fmt.Sprintf("%.2048v", runErr)})
		record := globalevents.LogRecord{
			Category:     globalevents.LogAudit,
			Level:        slog.LevelInfo,
			EventName:    "cron.trigger_completed",
			Module:       "cron",
			Type:         globalevents.ContentJSON,
			Content:      string(content),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultSucceeded,
			Identity:     identity,
		}
		if runErr != nil {
			record.EventName = "cron.trigger_failed"
			record.ResultStatus = globalevents.ResultFailed
			record.Level = slog.LevelWarn
			if globalevents.LogStatus(runErr) == globalevents.ResultCanceled {
				record.Level, record.ResultStatus = slog.LevelInfo, globalevents.ResultCanceled
			}
		}
		background.LogBackground(ctx, record)
	}
	return runErr
}

func cronLogIdentity(ctx context.Context) globalevents.LogIdentity {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	return globalevents.LogIdentityFromExecution(facts)
}

func normalizePlatformTargets(cliPlatformName string, targets []PlatformTarget) []PlatformTarget {
	seen := map[string]bool{}
	out := []PlatformTarget{{Name: cliPlatformName, SuperadminIDs: []string{"local"}}}
	seen[cliPlatformName] = true
	for _, target := range targets {
		if seen[target.Name] {
			continue
		}
		seen[target.Name] = true
		out = append(out, PlatformTarget{Name: target.Name, SuperadminIDs: slices.Clone(target.SuperadminIDs)})
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
	var platforms []string
	for _, target := range s.resolveDeliveryTargets(meta) {
		platforms = append(platforms, target.Platform)
	}
	return uniqueStrings(platforms)
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
