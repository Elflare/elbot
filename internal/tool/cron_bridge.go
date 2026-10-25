// 将 Cron 工具的存储请求交给 Storage，并发布已提交的任务定义变更。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/modelmgr"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/tool/builtin"
)

// CronChangedEvent 只标识需要重新读库的任务，不携带可能过期的定义快照。
type CronChangedEvent struct {
	Name string // 数据库中的完整任务名称；删除事件也使用此标识。
}

// ObserveCronChanged 的回调只更新内存或入队；连接由订阅方持有和关闭。
func (m *Supervisor) ObserveCronChanged(handler func(CronChangedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, signal.ErrClosed
	}
	return m.cronChanged.Observe(func(_ context.Context, event CronChangedEvent) error {
		return handler(event)
	}, options)
}

type cronBridge struct {
	repo    storage.CronJobRepository                      // 借用共享存储，不持有数据库生命周期。
	models  *modelmgr.Service                              // 借用模型选择校验能力。
	changed *signal.Signal[CronChangedEvent, signal.Empty] // Tool 实例拥有的任务定义变更信号。
}

func (b cronBridge) Upsert(ctx context.Context, req builtin.CronSaveRequest) (builtin.CronInfo, error) {
	definition := req.Definition
	name := normalizeCronName(definition.Name)
	if name == "" {
		return builtin.CronInfo{}, fmt.Errorf("cron name is required")
	}
	meta := storage.CronMetadata{
		Kind: storage.CronMetadataKind, Version: 2, Title: definition.Title,
		CreatedBy: storage.CronActor{ActorID: definition.Actor.ID, Platform: definition.Actor.Platform, PlatformUserID: definition.Actor.PlatformUserID, DisplayName: definition.Actor.DisplayName},
		Schedule:  storage.CronSchedule{Mode: storage.CronScheduleMode(definition.ScheduleMode), RunAt: definition.RunAt, CronExpr: definition.CronExpr},
		Trigger:   storage.CronTrigger{Mode: storage.CronTriggerMode(definition.TriggerMode), Message: definition.Message},
		Target:    storage.CronTarget{AllEnabledPlatforms: definition.AllEnabledPlatforms, SourcePlatform: definition.SourcePlatform},
		LLM:       storage.CronLLMMetadata{ToolListNames: definition.ToolListNames, ModelProvider: definition.ModelProvider, Model: definition.Model},
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return builtin.CronInfo{}, err
	}
	job, err := b.repo.Upsert(ctx, storage.UpsertCronJobRequest{
		Name: name, Handler: storage.CronHandlerName, Schedule: req.Schedule,
		Enabled: definition.Enabled, Metadata: string(data), NextRunAt: req.NextRunAt,
		ResetDelivery: req.ResetDelivery,
	})
	if err != nil {
		return builtin.CronInfo{}, err
	}
	if err := b.notify(ctx, job.Name); err != nil {
		return builtin.CronInfo{}, err
	}
	return cronInfo(*job)
}

func (b cronBridge) GetByName(ctx context.Context, name string) (builtin.CronInfo, error) {
	job, err := b.repo.GetByName(ctx, normalizeCronName(name))
	if err != nil {
		return builtin.CronInfo{}, err
	}
	return cronInfo(*job)
}

func (b cronBridge) List(ctx context.Context, includeDisabled bool) ([]builtin.CronInfo, error) {
	jobs, err := b.repo.List(ctx, includeDisabled)
	if err != nil {
		return nil, err
	}
	result := []builtin.CronInfo{}
	for _, job := range jobs {
		if job.Handler != storage.CronHandlerName {
			continue
		}
		info, err := cronInfo(job)
		if err != nil {
			return nil, err
		}
		result = append(result, info)
	}
	return result, nil
}

func (b cronBridge) DisableByName(ctx context.Context, name string) error {
	name = normalizeCronName(name)
	if err := b.repo.DisableByName(ctx, name); err != nil {
		return err
	}
	return b.notify(ctx, name)
}

func (b cronBridge) DeleteByName(ctx context.Context, name string) error {
	name = normalizeCronName(name)
	if err := b.repo.DeleteByName(ctx, name); err != nil {
		return err
	}
	return b.notify(ctx, name)
}

func (b cronBridge) ValidateSelection(provider, model string) error {
	return b.models.ValidateSelection(config.ModelSelection{Provider: provider, Model: model})
}

func normalizeCronName(name string) string {
	name = strings.TrimPrefix(strings.TrimSpace(name), "user.cron.")
	name = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`).ReplaceAllString(name, "_")
	name = strings.Trim(name, "._-")
	if name == "" {
		return ""
	}
	return "user.cron." + name
}

func cronInfo(job storage.CronJob) (builtin.CronInfo, error) {
	var meta storage.CronMetadata
	if err := json.Unmarshal([]byte(job.Metadata), &meta); err != nil {
		return builtin.CronInfo{}, err
	}
	if job.Handler != storage.CronHandlerName || meta.Kind != storage.CronMetadataKind {
		return builtin.CronInfo{}, fmt.Errorf("unsupported cron task %q", job.Name)
	}
	var status storage.CronTaskStatus
	if job.DeliveryState != "" {
		if err := json.Unmarshal([]byte(job.DeliveryState), &status); err != nil {
			return builtin.CronInfo{}, err
		}
	}
	return builtin.CronInfo{
		Name: job.Name, Title: meta.Title, ScheduleMode: string(meta.Schedule.Mode), RunAt: meta.Schedule.RunAt, CronExpr: meta.Schedule.CronExpr,
		TriggerMode: string(meta.Trigger.Mode), Message: meta.Trigger.Message, ToolListNames: meta.LLM.ToolListNames, ModelProvider: meta.LLM.ModelProvider, Model: meta.LLM.Model,
		AllEnabledPlatforms: meta.Target.AllEnabledPlatforms, SourcePlatform: meta.Target.SourcePlatform,
		CreatedBy: contextinfo.Actor{ID: meta.CreatedBy.ActorID, Platform: meta.CreatedBy.Platform, PlatformUserID: meta.CreatedBy.PlatformUserID, DisplayName: meta.CreatedBy.DisplayName},
		Enabled:   job.Enabled, Completed: meta.Schedule.Mode == storage.CronScheduleOnce && (!job.Enabled || status.DeliveryComplete),
		RunCount: job.RunCount, LastRunAt: job.LastRunAt, NextRunAt: job.NextRunAt, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}, nil
}

func (b cronBridge) notify(ctx context.Context, name string) error {
	// 写入已经提交；调用方此时取消也不能撤销变更或跳过通知。
	_, err := b.changed.Emit(context.WithoutCancel(ctx), CronChangedEvent{Name: name})
	if err != nil {
		return fmt.Errorf("cron %q 已写入，但变更通知失败: %w", name, err)
	}
	return nil
}
