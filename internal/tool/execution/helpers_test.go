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

func newTestManager(catalog *registry.Registry, permission *security.Permission, centers ...*media.MediaCenter) *Manager {
	if permission == nil {
		permission = security.DefaultPermission()
	}
	var center *media.MediaCenter
	if len(centers) > 0 {
		center = centers[0]
	}
	return NewManager(ManagerDeps{Registry: catalog, Permission: permission, Media: toolmedia.NewMediaRuntime(center, "", center.PublishUsageFinished), InfoAvailableInContext: registry.InfoAvailableInContext, UnavailableReason: registry.UnavailableReason, BackgroundDiscoveredTools: registry.BackgroundDiscoveredTools, DecodeState: state.DecodeState, PrepareUpdate: state.PrepareUpdate, DiscoveryStateUpdate: discovery.NewResolver(catalog).DiscoveryStateUpdate, Log: func(ctx context.Context, record events.LogRecord) {
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
	risk := t.risk
	if risk == "" {
		risk = share.RiskLow
	}
	return share.Info{Name: t.name, Description: t.name, Source: t.source, Risk: risk, SuperadminOnly: t.superadminOnly}
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

// 测试夹具使用空参数对象，只保留各场景的执行与权限行为。
func executionTestDefinition(candidate interface {
	Info() share.Info
	PrepareCall(context.Context, share.CallRequest) (*share.PreparedCall, error)
}) share.ToolDefinition {
	info := candidate.Info()
	info.Description = info.Name
	return share.ToolDefinition{Definition: info, Parameters: map[string]any{"type": "object", "properties": map[string]any{}}, Prepare: candidate.PrepareCall}
}

func (t fakeTool) Definition() share.ToolDefinition               { return executionTestDefinition(t) }
func (t foregroundOnlyTestTool) Definition() share.ToolDefinition { return executionTestDefinition(t) }
func (t runnerPreflightTool) Definition() share.ToolDefinition    { return executionTestDefinition(t) }
func (t runnerContextPreflightTool) Definition() share.ToolDefinition {
	return executionTestDefinition(t)
}
func (t runnerImageTool) Definition() share.ToolDefinition        { return executionTestDefinition(t) }
func (t runnerHighRiskTool) Definition() share.ToolDefinition     { return executionTestDefinition(t) }
func (t loggingTool) Definition() share.ToolDefinition            { return executionTestDefinition(t) }
func (t partialResultTool) Definition() share.ToolDefinition      { return executionTestDefinition(t) }
func (t segmentTool) Definition() share.ToolDefinition            { return executionTestDefinition(t) }
func (t countingCommitTool) Definition() share.ToolDefinition     { return executionTestDefinition(t) }
func (t *mediaArgumentRecorder) Definition() share.ToolDefinition { return executionTestDefinition(t) }
func (t failingMediaTool) Definition() share.ToolDefinition       { return executionTestDefinition(t) }
func (t *snapshotTool) Definition() share.ToolDefinition          { return executionTestDefinition(t) }
func (t concurrentTool) Definition() share.ToolDefinition         { return executionTestDefinition(t) }
func (t availabilityTestTool) Definition() share.ToolDefinition   { return executionTestDefinition(t) }
func (t blockingAuditTool) Definition() share.ToolDefinition      { return executionTestDefinition(t) }
func (t ruleDetailTool) Definition() share.ToolDefinition {
	d := executionTestDefinition(t)
	d.LoadDetail = t.LoadDetail
	return d
}

// 多步测试装配仍提交完整组，避免测试构造依赖逐项注册的旧接口。
func registerTestTools(catalog *registry.Registry, candidates ...share.ToolDefinition) error {
	definitions, err := catalog.Query(context.Background(), share.ToolQuery{Scope: share.QueryCatalog, IncludeHidden: true, IncludeDiscover: true})
	if err != nil {
		return err
	}
	definitions = append(definitions, candidates...)
	_, err = catalog.RegisterTools(context.Background(), share.SourceSkill, share.ToolRegistrationScope{}, definitions)
	return err
}
