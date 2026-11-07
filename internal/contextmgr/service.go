package contextmgr

import (
	"context"
	"sync"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

// ModelCapabilities 只声明查询模型窗口所需的选择解析能力。
type ModelCapabilities interface {
	Resolve(config.ModelSelection) modelmgr.Selection
}

type Options struct {
	Compactors CompactorResolver
	Store      storage.Store
	Models     ModelCapabilities
	Config     config.ContextConfig
}

type Service struct {
	compactors   CompactorResolver
	store        storage.Store
	models       ModelCapabilities
	usageWriteMu sync.Mutex
	config       config.ContextConfig
}

func New(opts Options) *Service {
	return &Service{compactors: opts.Compactors, store: opts.Store, models: opts.Models, config: opts.Config}
}

func (s *Service) Load(ctx context.Context, id string) (*LoadedContext, error) {
	return (Loader{Store: s.store}).Load(ctx, id)
}

// RecordTurn 在 Turn 的 loop 和回复提交成功后，一次保存活动时间与用量；nil 用量保留原值。
// 当前用量由 StatusRecorder 持有；这里仅持久化调用方提供的快照。
func (s *Service) RecordTurn(ctx context.Context, id string, usage *llm.Usage) error {
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

func (r *Service) Status(ctx context.Context, usage *llm.Usage, selection config.ModelSelection) UsageState {
	return UsageState{Usage: usage, ContextWindow: r.models.Resolve(selection).ContextWindow(ctx), TriggerRatio: r.config.CompactTriggerRatio, CompactEnabled: r.config.CompactEnabled}
}

func (r *Service) ReachedCompactThreshold(ctx context.Context, usage *llm.Usage, selection modelmgr.Selection) bool {
	ctxCfg := r.config
	if !ctxCfg.CompactEnabled || usage == nil || usage.TotalTokens == nil || *usage.TotalTokens < 0 {
		return false
	}
	window := selection.ContextWindow(ctx)
	state := UsageState{Usage: usage, ContextWindow: window, TriggerRatio: ctxCfg.CompactTriggerRatio}
	return state.ReachedThreshold()
}
