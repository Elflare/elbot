package execution

import (
	"context"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/discovery"
	toolmedia "elbot/internal/tool/media"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
	"elbot/internal/tool/state"
)

func newTestManager(catalog *registry.Registry, permission *security.Permission, centers ...*media.Manager) *Manager {
	if permission == nil {
		permission = security.DefaultPermission()
	}
	var center *media.Manager
	if len(centers) > 0 {
		center = centers[0]
	}
	return NewManager(ManagerDeps{Registry: catalog, Permission: permission, Media: toolmedia.NewMediaRuntime(center, ""), InfoAvailableInContext: registry.InfoAvailableInContext, UnavailableReason: registry.UnavailableReason, BackgroundDiscoveredTools: registry.BackgroundDiscoveredTools, DecodeState: state.DecodeState, PrepareUpdate: state.PrepareUpdate, DiscoveryStateUpdate: discovery.NewResolver(catalog).DiscoveryStateUpdate, Log: func(ctx context.Context, record events.LogRecord) {
		_ = events.EmitLog(context.WithoutCancel(ctx), record)
	}})
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
func (t fakeTool) PrepareCall(ctx context.Context, req share.CallRequest) (*share.PreparedCall, error) {
	return share.NewPreparedCall(req, share.RiskAssessment{Level: t.Info().Risk}, "", func(ctx context.Context) (*share.Result, string, error) {
		return &share.Result{Content: t.name}, t.name, nil
	}), nil
}

func executeTestCall(catalog *registry.Registry, call llm.ToolCallRequest) executionResult {
	m := newTestManager(catalog, nil)
	ctx := context.Background()
	resolved := m.resolve(ctx, call.Name, nil, storage.SessionModeWork)
	var prepared *share.PreparedCall
	if resolved.Native != nil {
		var err error
		prepared, err = m.prepareCall(ctx, resolved, call)
		if err != nil {
			panic(err)
		}
	}
	return m.executePrepared(ctx, call, resolved, prepared, nil)
}
