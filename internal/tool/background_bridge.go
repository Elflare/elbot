// 接收 Background 公开的 Cron 能力，转换为 Tool 内部契约并注册 Cron 工具。
// 注册关系由 Tool 持有并随自身生命周期释放。
package tool

import (
	"context"
	"fmt"

	"elbot/internal/background"
	"elbot/internal/contextinfo"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/share"
)

// ConnectBackground 在启动前接入一次后台任务能力；Tool 持有注册关系，不持有后台生命周期。
func (m *Supervisor) ConnectBackground(backend interface {
	CreateCron(context.Context, background.CronCreateRequest) (background.CronInfo, error)
	UpdateCron(context.Context, background.CronUpdateRequest) (background.CronInfo, error)
	GetCron(context.Context, string, contextinfo.Actor) (background.CronInfo, error)
	ListCrons(context.Context, bool, bool, contextinfo.Actor) ([]background.CronInfo, error)
	DisableCron(context.Context, string, contextinfo.Actor) error
	DeleteCron(context.Context, string, contextinfo.Actor) error
}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("tool supervisor is closing")
	}
	if m.started {
		return fmt.Errorf("background must be connected before tool start")
	}
	if m.releaseBackgroundTools != nil {
		return fmt.Errorf("background is already connected")
	}
	release, err := m.catalog.registry.RegisterOwned(builtin.NewCronTools(backgroundBridge{backend: backend})...)
	if err != nil {
		return err
	}
	m.releaseBackgroundTools = release
	return nil
}

type backgroundBridge struct {
	backend interface {
		CreateCron(context.Context, background.CronCreateRequest) (background.CronInfo, error)
		UpdateCron(context.Context, background.CronUpdateRequest) (background.CronInfo, error)
		GetCron(context.Context, string, contextinfo.Actor) (background.CronInfo, error)
		ListCrons(context.Context, bool, bool, contextinfo.Actor) ([]background.CronInfo, error)
		DisableCron(context.Context, string, contextinfo.Actor) error
		DeleteCron(context.Context, string, contextinfo.Actor) error
	} // 借用 Background 总管公开的任务管理能力。
}

func (b backgroundBridge) CreateCron(ctx context.Context, req share.CronCreateRequest) (share.CronInfo, error) {
	info, err := b.backend.CreateCron(ctx, background.CronCreateRequest{
		Name: req.Name, Title: req.Title, ScheduleMode: req.ScheduleMode,
		RunAt: req.RunAt, CronExpr: req.CronExpr, TriggerMode: req.TriggerMode,
		Message: req.Message, ToolListNames: req.ToolListNames,
		ModelProvider: req.ModelProvider, Model: req.Model,
		AllEnabledPlatforms: req.AllEnabledPlatforms, Enabled: req.Enabled,
		Actor: req.Actor, SourcePlatform: req.SourcePlatform,
	})
	if err != nil {
		return share.CronInfo{}, err
	}
	return cronInfo(info), nil
}

func (b backgroundBridge) UpdateCron(ctx context.Context, req share.CronUpdateRequest) (share.CronInfo, error) {
	info, err := b.backend.UpdateCron(ctx, background.CronUpdateRequest{
		Name: req.Name, Title: req.Title, ScheduleMode: req.ScheduleMode,
		RunAt: req.RunAt, CronExpr: req.CronExpr, TriggerMode: req.TriggerMode,
		Message: req.Message, ToolListNames: req.ToolListNames,
		ModelProvider: req.ModelProvider, Model: req.Model,
		AllEnabledPlatforms: req.AllEnabledPlatforms, Enabled: req.Enabled, Actor: req.Actor,
	})
	if err != nil {
		return share.CronInfo{}, err
	}
	return cronInfo(info), nil
}

func (b backgroundBridge) GetCron(ctx context.Context, name string, actor contextinfo.Actor) (share.CronInfo, error) {
	info, err := b.backend.GetCron(ctx, name, actor)
	if err != nil {
		return share.CronInfo{}, err
	}
	return cronInfo(info), nil
}

func (b backgroundBridge) ListCrons(ctx context.Context, includeDisabled, includeCompleted bool, actor contextinfo.Actor) ([]share.CronInfo, error) {
	items, err := b.backend.ListCrons(ctx, includeDisabled, includeCompleted, actor)
	if err != nil {
		return nil, err
	}
	result := make([]share.CronInfo, len(items))
	for i, item := range items {
		result[i] = cronInfo(item)
	}
	return result, nil
}

func (b backgroundBridge) DisableCron(ctx context.Context, name string, actor contextinfo.Actor) error {
	return b.backend.DisableCron(ctx, name, actor)
}

func (b backgroundBridge) DeleteCron(ctx context.Context, name string, actor contextinfo.Actor) error {
	return b.backend.DeleteCron(ctx, name, actor)
}

func cronInfo(info background.CronInfo) share.CronInfo {
	return share.CronInfo{
		Name: info.Name, Title: info.Title, ScheduleMode: info.ScheduleMode,
		RunAt: info.RunAt, CronExpr: info.CronExpr, TriggerMode: info.TriggerMode,
		Message: info.Message, ToolListNames: append([]string(nil), info.ToolListNames...),
		ModelProvider: info.ModelProvider, Model: info.Model,
		AllEnabledPlatforms: info.AllEnabledPlatforms, SourcePlatform: info.SourcePlatform,
		CreatedBy: info.CreatedBy, Enabled: info.Enabled, Completed: info.Completed,
		RunCount: info.RunCount, LastRunAt: info.LastRunAt, NextRunAt: info.NextRunAt,
		CreatedAt: info.CreatedAt, UpdatedAt: info.UpdatedAt,
	}
}
