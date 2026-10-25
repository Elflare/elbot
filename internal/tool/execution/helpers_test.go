package execution

import (
	"context"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

func newTestManager(catalog *registry.Registry, permission *security.Permission, centers ...*media.Manager) *Manager {
	if permission == nil {
		permission = security.DefaultPermission()
	}
	var center *media.Manager
	if len(centers) > 0 {
		center = centers[0]
	}
	return NewManager(catalog, permission, center, func(ctx context.Context, record events.LogRecord) {
		_ = events.EmitLog(context.WithoutCancel(ctx), record)
	})
}

type fakeTool struct {
	name           string
	source         share.Source
	risk           share.RiskLevel
	superadminOnly bool
}

func (t fakeTool) Name() string { return t.name }
func (t fakeTool) Info() share.Info {
	return share.Info{Name: t.name, Source: t.source, Risk: t.risk, SuperadminOnly: t.superadminOnly}
}
func (t fakeTool) Schema() llm.ToolSchema { return llm.ToolSchema{Name: t.name} }
func (t fakeTool) Call(context.Context, share.CallRequest) (*share.Result, string, error) {
	return &share.Result{Content: t.name}, t.name, nil
}

func executeTestCall(catalog *registry.Registry, call llm.ToolCallRequest) executionResult {
	m := newTestManager(catalog, nil)
	ctx := context.Background()
	return m.executePrepared(ctx, call, m.resolve(ctx, call.Name, nil, storage.SessionModeWork), nil, nil)
}
