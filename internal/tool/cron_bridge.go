// 将 Cron 工具的存储请求交给 Storage，并发布已提交的任务定义变更。
package tool

import (
	"context"
	"elbot/internal/crondef"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
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
	return m.cronChanged.Connect(func(_ context.Context, event CronChangedEvent) (signal.Empty, error) {
		return signal.Empty{}, handler(event)
	}, options)
}

// cronStore 是 Tool 的任务定义读写边界；运行状态和投递由 Background 管理。
type cronStore interface {
	UpsertCronJob(context.Context, storage.UpsertCronJobRequest) (*storage.CronJob, error)
	GetByName(context.Context, string) (*storage.CronJob, error)
	ListCronJobs(context.Context, bool) ([]storage.CronJob, error)
	DisableByName(context.Context, string) error
	DeleteByName(context.Context, string) error
}

type cronBridge struct {
	repo    cronStore                                      // 借用共享存储，不持有数据库生命周期。
	models  ModelCapabilities                              // 借用模型选择校验能力。
	changed *signal.Signal[CronChangedEvent, signal.Empty] // Tool 实例拥有的任务定义变更信号。
}

func (b cronBridge) Upsert(ctx context.Context, req builtin.CronSaveRequest) (operationresult.Result[builtin.CronInfo], error) {
	definition := req.Definition
	name := normalizeCronName(definition.Name)
	if name == "" {
		return operationresult.Result[builtin.CronInfo]{Status: operationresult.InvalidArguments, Message: "cron name is required"}, nil
	}
	meta := crondef.Metadata{
		Kind: crondef.MetadataKind, Version: 2, Title: definition.Title,
		CreatedBy: crondef.CronActor{Platform: definition.Actor.Platform, ID: definition.Actor.ID, Name: definition.Actor.Name},
		Schedule:  crondef.CronSchedule{Mode: crondef.ScheduleMode(definition.ScheduleMode), RunAt: definition.RunAt, CronExpr: definition.CronExpr},
		Trigger:   crondef.CronTrigger{Mode: crondef.TriggerMode(definition.TriggerMode), Message: definition.Message},
		Target:    crondef.CronTarget{ID: definition.TargetID, AllEnabledPlatforms: definition.AllEnabledPlatforms, SourcePlatform: definition.SourcePlatform},
		LLM:       crondef.CronLLMMetadata{ToolListNames: definition.ToolListNames, ModelProvider: definition.ModelProvider, Model: definition.Model},
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return operationresult.Result[builtin.CronInfo]{}, err
	}
	job, err := b.repo.UpsertCronJob(ctx, storage.UpsertCronJobRequest{
		Name: name, Handler: crondef.UserHandlerName, Schedule: req.Schedule,
		Enabled: definition.Enabled, Metadata: string(data), NextRunAt: req.NextRunAt,
		ResetDelivery: req.ResetDelivery,
	})
	if err != nil {
		return operationresult.Result[builtin.CronInfo]{}, err
	}
	if err := b.notify(ctx, job.Name); err != nil {
		return operationresult.Result[builtin.CronInfo]{}, err
	}
	return cronInfo(*job)
}

func (b cronBridge) GetByName(ctx context.Context, name string) (operationresult.Result[builtin.CronInfo], error) {
	job, err := b.repo.GetByName(ctx, normalizeCronName(name))
	if errors.Is(err, storage.ErrNotFound) {
		return operationresult.Result[builtin.CronInfo]{Status: operationresult.NotFound, Message: fmt.Sprintf("定时任务 %q 不存在", name)}, nil
	}
	if err != nil {
		return operationresult.Result[builtin.CronInfo]{}, err
	}
	return cronInfo(*job)
}

func (b cronBridge) List(ctx context.Context, includeDisabled bool) (operationresult.Result[[]builtin.CronInfo], error) {
	jobs, err := b.repo.ListCronJobs(ctx, includeDisabled)
	if err != nil {
		return operationresult.Result[[]builtin.CronInfo]{}, err
	}
	result := []builtin.CronInfo{}
	for _, job := range jobs {
		if job.Handler != crondef.UserHandlerName {
			continue
		}
		info, err := cronInfo(job)
		if err != nil {
			return operationresult.Result[[]builtin.CronInfo]{}, err
		}
		if info.Status != operationresult.Success {
			return operationresult.Result[[]builtin.CronInfo]{Status: info.Status, Message: info.Message}, nil
		}
		result = append(result, info.Data)
	}
	return operationresult.Result[[]builtin.CronInfo]{Status: operationresult.Success, Data: result}, nil
}

func (b cronBridge) DisableByName(ctx context.Context, name string) (operationresult.Result[struct{}], error) {
	name = normalizeCronName(name)
	if err := b.repo.DisableByName(ctx, name); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: fmt.Sprintf("定时任务 %q 不存在", name)}, nil
		}
		return operationresult.Result[struct{}]{}, err
	}
	if err := b.notify(ctx, name); err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (b cronBridge) DeleteByName(ctx context.Context, name string) (operationresult.Result[struct{}], error) {
	name = normalizeCronName(name)
	if err := b.repo.DeleteByName(ctx, name); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: fmt.Sprintf("定时任务 %q 不存在", name)}, nil
		}
		return operationresult.Result[struct{}]{}, err
	}
	if err := b.notify(ctx, name); err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (b cronBridge) ValidateSelection(provider, model string) operationresult.Result[struct{}] {
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

func cronInfo(job storage.CronJob) (operationresult.Result[builtin.CronInfo], error) {
	var meta crondef.Metadata
	if err := json.Unmarshal([]byte(job.Metadata), &meta); err != nil {
		return operationresult.Result[builtin.CronInfo]{}, err
	}
	if job.Handler != crondef.UserHandlerName || meta.Kind != crondef.MetadataKind {
		return operationresult.Result[builtin.CronInfo]{Status: operationresult.Incompatible, Message: fmt.Sprintf("unsupported cron task %q", job.Name)}, nil
	}
	var status crondef.CronTaskStatus
	if job.DeliveryState != "" {
		if err := json.Unmarshal([]byte(job.DeliveryState), &status); err != nil {
			return operationresult.Result[builtin.CronInfo]{}, err
		}
	}
	return operationresult.Result[builtin.CronInfo]{Status: operationresult.Success, Data: builtin.CronInfo{
		Name: job.Name, Title: meta.Title, ScheduleMode: string(meta.Schedule.Mode), RunAt: meta.Schedule.RunAt, CronExpr: meta.Schedule.CronExpr,
		TriggerMode: string(meta.Trigger.Mode), Message: meta.Trigger.Message, ToolListNames: meta.LLM.ToolListNames, ModelProvider: meta.LLM.ModelProvider, Model: meta.LLM.Model,
		TargetID: meta.Target.ID, AllEnabledPlatforms: meta.Target.AllEnabledPlatforms, SourcePlatform: meta.Target.SourcePlatform,
		CreatedBy: contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: meta.CreatedBy.Platform, ID: meta.CreatedBy.ID}, Name: meta.CreatedBy.Name},
		Enabled:   job.Enabled, Completed: meta.Schedule.Mode == crondef.ScheduleOnce && (!job.Enabled || status.DeliveryComplete),
		RunCount: job.RunCount, LastRunAt: job.LastRunAt, NextRunAt: job.NextRunAt, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}}, nil
}

func (b cronBridge) notify(ctx context.Context, name string) error {
	// 写入已经提交；调用方此时取消也不能撤销变更或跳过通知。
	results, err := b.changed.Emit(context.WithoutCancel(ctx), CronChangedEvent{Name: name})
	if err != nil {
		return fmt.Errorf("cron %q 已写入，但变更通知失败: %w", name, err)
	}
	for _, result := range results {
		if result.Err != nil {
			return fmt.Errorf("cron %q 已写入，但变更通知失败: %w", name, result.Err)
		}
	}
	return nil
}
