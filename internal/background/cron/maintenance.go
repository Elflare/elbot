package cron

import (
	"context"
	"elbot/internal/operationresult"
	"errors"

	"elbot/internal/background/share"
)

// RegisterMaintenanceHandler 将无需任务记录的维护回调接入 Cron。
func (m *Manager) RegisterMaintenanceHandler(name string, handler func(context.Context) error) {
	m.RegisterHandler(name, func(ctx context.Context, _ Job) (context.Context, operationresult.Result[struct{}], error) {
		err := handler(ctx)
		if err != nil {
			return ctx, operationresult.Result[struct{}]{}, err
		}
		return ctx, operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	})
}

// ConfigureMaintenanceJob 应用维护配置，不启动调度器。
func (m *Manager) ConfigureMaintenanceJob(ctx context.Context, req share.MaintenanceJob) error {
	if req.Enabled {
		_, err := m.UpsertJob(ctx, UpsertJobRequest{Name: req.Name, Handler: req.Handler, Schedule: req.Schedule, Enabled: true})
		return err
	}
	if err := m.DisableJob(ctx, req.Name); err != nil && !errors.Is(err, ErrJobNotFound) {
		return err
	}
	return nil
}
