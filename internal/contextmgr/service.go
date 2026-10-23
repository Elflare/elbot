package contextmgr

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

type Options struct {
	Compactors CompactorResolver
	Store      storage.Store
	Models     *modelmgr.Service
	Config     config.ContextConfig
}

type Service struct {
	compactors   CompactorResolver
	store        storage.Store
	models       *modelmgr.Service
	loader       Loader
	mu           sync.Mutex
	usageWriteMu sync.Mutex
	config       config.ContextConfig
}

func New(opts Options) *Service {
	s := &Service{compactors: opts.Compactors, store: opts.Store, models: opts.Models, loader: Loader{Store: opts.Store}}
	s.Configure(opts.Config)
	return s
}

func (s *Service) Configure(cfg config.ContextConfig) {
	s.mu.Lock()
	s.config = cfg
	s.mu.Unlock()
}

func (s *Service) Load(ctx context.Context, id string) (*LoadedContext, error) {
	return s.loader.Load(ctx, id)
}

func cloneUsage(usage *llm.Usage) *llm.Usage {
	if usage == nil {
		return nil
	}
	copy := *usage
	if usage.PromptTokens != nil {
		copy.PromptTokens = new(int64)
		*copy.PromptTokens = *usage.PromptTokens
	}
	if usage.CompletionTokens != nil {
		copy.CompletionTokens = new(int64)
		*copy.CompletionTokens = *usage.CompletionTokens
	}
	if usage.TotalTokens != nil {
		copy.TotalTokens = new(int64)
		*copy.TotalTokens = *usage.TotalTokens
	}
	if usage.CacheHitTokens != nil {
		copy.CacheHitTokens = new(int64)
		*copy.CacheHitTokens = *usage.CacheHitTokens
	}
	return &copy
}

// RecordTurn 在 Turn 的 loop 和回复提交成功后，一次保存活动时间与用量；nil 用量保留原值。
// 当前用量由 StatusRecorder 持有；这里仅持久化调用方提供的快照。
func (s *Service) RecordTurn(ctx context.Context, id string, usage *llm.Usage) error {
	if id == "" {
		return nil
	}
	usage = cloneUsage(usage)
	s.usageWriteMu.Lock()
	defer s.usageWriteMu.Unlock()
	_, err := s.store.Sessions().Mutate(ctx, id, func(row *storage.Session) error {
		row.UpdatedAt = storage.Now()
		if usage == nil {
			return nil
		}
		if _, err := DecodeState(row.Metadata); err != nil {
			return err
		}
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		if err := fields.Set("last_usage", usage); err != nil {
			return err
		}
		row.Metadata, err = fields.Encode()
		return err
	})
	return err
}

func (r *Service) Status(ctx context.Context, usage *llm.Usage, selection config.ModelSelection) string {
	r.mu.Lock()
	ctxCfg := r.config
	r.mu.Unlock()

	window := r.models.Resolve(selection).ContextWindow(ctx)
	threshold := ctxCfg.CompactTriggerRatio
	var sb strings.Builder
	sb.WriteString("  ")
	sb.WriteString(FormatTokens(usage))
	sb.WriteString("\n")
	if usage == nil || usage.TotalTokens == nil || *usage.TotalTokens < 0 || window <= 0 {
		sb.WriteString(fmt.Sprintf("  context window: %d (unknown)\n", window))
		status := "unknown"
		if !ctxCfg.CompactEnabled {
			status = "disabled"
		}
		sb.WriteString(fmt.Sprintf("  compact threshold: %.0f%%, status: %s\n", threshold*100, status))
		return sb.String()
	}
	ratio := float64(*usage.TotalTokens) / float64(window)
	sb.WriteString(fmt.Sprintf("  context window: %d (%.1f%%)\n", window, ratio*100))
	status := "ok"
	if !ctxCfg.CompactEnabled {
		status = "disabled"
	} else if ratio >= threshold {
		status = "will compact before next request"
	} else if ratio >= math.Max(0, threshold-0.1) {
		status = "near threshold"
	}
	sb.WriteString(fmt.Sprintf("  compact threshold: %.0f%%, status: %s\n", threshold*100, status))
	return sb.String()
}

func (r *Service) ReachedCompactThreshold(ctx context.Context, usage *llm.Usage, selection modelmgr.Selection) bool {
	r.mu.Lock()
	ctxCfg := r.config
	r.mu.Unlock()
	if !ctxCfg.CompactEnabled || usage == nil || usage.TotalTokens == nil || *usage.TotalTokens < 0 {
		return false
	}
	window := selection.ContextWindow(ctx)
	state := UsageState{Usage: usage, ContextWindow: window, TriggerRatio: ctxCfg.CompactTriggerRatio}
	return state.ReachedThreshold()
}
