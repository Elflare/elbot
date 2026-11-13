package execution

import (
	"context"
	"elbot/internal/operationresult"

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
		events.Log(context.WithoutCancel(ctx), record)
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
func (t fakeTool) PrepareCall(ctx context.Context, req share.CallRequest) (operationresult.Result[*share.PreparedCall], error) {
	return operationresult.Result[*share.PreparedCall]{Status: operationresult.Success, Data: share.NewPreparedCall(req, share.RiskAssessment{Level: t.Info().Risk}, nil, func(ctx context.Context) (operationresult.Result[*share.Result], string, error) {
		return operationresult.Result[*share.Result]{Status: operationresult.Success, Data: &share.Result{Content: t.name}}, t.name, nil
	})}, nil
}

func executeTestCall(catalog *registry.Registry, call llm.ToolCallRequest) executionResult {
	m := newTestManager(catalog, nil)
	ctx := context.Background()
	resolved, err := m.resolve(ctx, call.Name, nil, storage.SessionModeWork)
	if err != nil {
		return executionError(call, err)
	}
	if resolved.Status != operationresult.Success {
		return executionResult{Message: toolMessage(call.Name, call.ID, resolved.Message), Result: operationresult.Result[*share.Result]{Status: resolved.Status, Message: resolved.Message}}
	}
	prepared, err := m.prepareCall(ctx, resolved.Data, call)
	if err != nil {
		return executionError(call, err)
	}
	if prepared.Status != operationresult.Success {
		return executionResult{Message: toolMessage(call.Name, call.ID, prepared.Message), Result: operationresult.Result[*share.Result]{Status: prepared.Status, Message: prepared.Message}}
	}
	return m.executePrepared(ctx, call, resolved.Data, prepared.Data, nil)
}

// 测试夹具使用空参数对象，只保留各场景的执行与权限行为。
func executionTestDefinition(candidate interface {
	Info() share.Info
	PrepareCall(context.Context, share.CallRequest) (operationresult.Result[*share.PreparedCall], error)
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
