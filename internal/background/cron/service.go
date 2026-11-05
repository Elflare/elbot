package cron

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"elbot/internal/agent"
	background "elbot/internal/background/share"
	"elbot/internal/config"
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
	log             func(context.Context, globalevents.LogRecord) // 根部注入的异步日志出口。
	cliPlatformName string                                        // 本地平台身份。
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
	Log             func(context.Context, globalevents.LogRecord) // 发布由调用处组织的完整日志。
	CLIPlatformName string                                        // 根部注入的本地平台身份，用于默认目标和恢复通知。
	NewRunID        func(context.Context) (string, error)         // 根部提供运行批次身份。
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
	s := &Service{log: opts.Log,
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

func (s *Service) Handler(ctx context.Context, job Job) (context.Context, error) {
	identity := globalevents.LogIdentityFromContext(ctx)

	unlock, err := s.lockDeliveryJob(ctx, job.Name)
	if err != nil {
		return ctx, err
	}
	defer unlock()

	latest, err := s.store.GetByName(ctx, job.Name)
	if err != nil {
		return ctx, err
	}
	if !latest.Enabled {
		return ctx, nil
	}
	job = *latest

	meta, err := decodeMetadata(job.Metadata)
	if err != nil {
		return ctx, err
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
		globalevents.Log(ctx, record)
	}

	return s.runTask(ctx, job, meta)
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
