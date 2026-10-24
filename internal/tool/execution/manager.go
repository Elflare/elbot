// 解析工具执行来源，调度原生与 Elwisp 调用。
package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type Manager struct {
	Native     *NativeSource
	Permission *security.Permission
	Media      *media.Manager
	Log        func(context.Context, events.LogRecord) // Root-owned publication for complete execution records.
}

type ResolvedTool struct {
	// Name is the resolved execution name; failed lookup may retain the requested name.
	Name string
	// Source identifies the selected provider; empty means no provider was found.
	Source toolshare.SourceKind
	// Native is the selected native implementation, nil for remote or unavailable tools.
	Native toolshare.Tool
	// Discovered 是本次选中的已发现工具声明；直接解析的前台工具没有此声明。
	Discovered *toolshare.DiscoveredTool
	// ResultStatus is declared by Resolve: succeeded, rejected by permission, or failed lookup/configuration.
	ResultStatus events.ResultStatus
	// Error retains the resolution error, including typed permission denial; nil does not declare success.
	Error error
}

func NewManager(registry *tool.Registry, permission *security.Permission) *Manager {
	return &Manager{Native: NewNativeSource(registry), Permission: permission}
}

func (m *Manager) ToolInfos(ctx context.Context, view toolshare.Context) ([]toolshare.Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if view.DisableBaseTools || view.Mode != storage.SessionModeWork {
		return nil, nil
	}
	return m.Native.ToolInfos(ctx, actorForView(ctx, view), m.Permission), nil
}

func (m *Manager) ToolNames(ctx context.Context, view toolshare.Context) ([]string, error) {
	infos, err := m.ToolInfos(ctx, view)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names, nil
}

func (m *Manager) BaseSchemas(ctx context.Context, view toolshare.Context) ([]llm.ToolSchema, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if view.DisableBaseTools || view.Mode != storage.SessionModeWork {
		return nil, nil
	}
	return m.Native.BaseSchemas(ctx), nil
}

func (m *Manager) Schemas(ctx context.Context, view toolshare.Context, discovered []toolshare.DiscoveredTool) ([]llm.ToolSchema, error) {
	if view.Mode != storage.SessionModeWork && view.Mode != storage.SessionModeBackground {
		return nil, nil
	}
	base, err := m.BaseSchemas(ctx, view)
	if err != nil {
		return nil, err
	}
	out := make([]llm.ToolSchema, 0, len(base)+len(discovered))
	seen := map[string]bool{}
	appendSchema := func(schema llm.ToolSchema) {
		name := schema.Name
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, cloneSchema(schema))
	}
	for _, schema := range base {
		appendSchema(schema)
	}
	for _, discoveredTool := range discovered {
		if view.Mode == storage.SessionModeBackground && !backgroundToolAllowed(ctx, discoveredTool) {
			continue
		}
		if !discoveredToolAvailable(ctx, discoveredTool) {
			continue
		}
		appendSchema(discoveredTool.Schema)
	}
	return out, nil
}

func discoveredToolAvailable(ctx context.Context, discovered toolshare.DiscoveredTool) bool {
	return AvailableInContext(ctx, toolshare.Info{ForegroundOnly: discovered.ForegroundOnly})
}

func (m *Manager) Resolve(ctx context.Context, name string, discovered []toolshare.DiscoveredTool, mode string) ResolvedTool {
	name = strings.TrimSpace(name)
	if name == "" {
		return ResolvedTool{Name: name, ResultStatus: events.ResultFailed, Error: fmt.Errorf("tool name is empty")}
	}
	for _, discoveredTool := range discovered {
		if discoveredTool.Name != name && discoveredTool.CanonicalName != name {
			continue
		}
		if mode == storage.SessionModeBackground && !backgroundToolAllowed(ctx, discoveredTool) {
			continue
		}
		discoveredCopy := discoveredTool
		if !discoveredToolAvailable(ctx, discoveredCopy) {
			return ResolvedTool{Name: name, Source: discoveredCopy.Source, Discovered: &discoveredCopy, ResultStatus: events.ResultRejected, Error: toolshare.PolicyDeniedError("tool is unavailable in this context")}
		}
		source := discoveredCopy.Source
		if source == toolshare.SourceKindELwisp {
			if discoveredCopy.Endpoint == "" {
				return ResolvedTool{Name: name, Source: source, Discovered: &discoveredCopy, ResultStatus: events.ResultFailed, Error: fmt.Errorf("ELwisp tool endpoint is missing")}
			}
			return ResolvedTool{Name: discoveredCopy.CanonicalName, Source: source, Discovered: &discoveredCopy, ResultStatus: events.ResultSucceeded}
		}
		if nativeTool, ok := m.Native.Get(discoveredCopy.Name); ok {
			return ResolvedTool{Name: discoveredCopy.Name, Source: toolshare.SourceKindNative, Native: nativeTool, Discovered: &discoveredCopy, ResultStatus: events.ResultSucceeded}
		}
		return ResolvedTool{Name: discoveredCopy.Name, Source: toolshare.SourceKindNative, Discovered: &discoveredCopy, ResultStatus: events.ResultFailed, Error: fmt.Errorf("native tool is no longer available")}
	}
	if mode == storage.SessionModeBackground {
		return ResolvedTool{Name: name, ResultStatus: events.ResultRejected, Error: toolshare.PolicyDeniedError("tool is not allowed in this background task")}
	}
	if nativeTool, ok := m.Native.Get(name); ok {
		if !AvailableInContext(ctx, nativeTool.Info()) {
			return ResolvedTool{Name: name, Source: toolshare.SourceKindNative, Native: nativeTool, ResultStatus: events.ResultRejected, Error: toolshare.PolicyDeniedError(unavailableReason(ctx, nativeTool.Info()))}
		}
		return ResolvedTool{Name: name, Source: toolshare.SourceKindNative, Native: nativeTool, ResultStatus: events.ResultSucceeded}
	}
	return ResolvedTool{Name: name, ResultStatus: events.ResultFailed, Error: fmt.Errorf("tool %q not found", name)}
}

func (m *Manager) AssessRisk(ctx context.Context, resolved ResolvedTool, arguments string) (toolshare.RiskAssessment, error) {
	if resolved.Source == toolshare.SourceKindELwisp {
		return toolshare.RiskAssessment{Level: toolshare.RiskLow}, nil
	}
	return tool.AssessRisk(ctx, resolved.Native, toolshare.CallRequest{Name: resolved.Name, Arguments: json.RawMessage(arguments)})
}

func (m *Manager) PrepareCall(ctx context.Context, resolved ResolvedTool, call llm.ToolCallRequest) (*tool.PreparedCall, error) {
	if resolved.Source == toolshare.SourceKindELwisp {
		return nil, nil
	}
	return tool.PrepareCall(ctx, resolved.Native, toolshare.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
}

func (m *Manager) Execute(ctx context.Context, call llm.ToolCallRequest, resolved ResolvedTool, actor contextinfo.Actor) toolshare.ExecutionResult {
	return m.ExecutePrepared(ctx, call, resolved, actor, nil, nil)
}

func (m *Manager) ExecutePrepared(ctx context.Context, call llm.ToolCallRequest, resolved ResolvedTool, actor contextinfo.Actor, prepared *tool.PreparedCall, onStarted func()) toolshare.ExecutionResult {

	message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID}
	if resolved.ResultStatus != events.ResultSucceeded {
		return executionError(call, message, resolved.Error, resolved.ResultStatus)
	}
	if resolved.Source == toolshare.SourceKindELwisp {
		return executeELwispTool(ctx, call, resolved, onStarted)
	}
	result := tool.Executor{Registry: m.Native.Registry, Actor: actor, Permission: m.Permission, OnStarted: onStarted}.ExecutePrepared(ctx, call, prepared)
	return toolshare.ExecutionResult{Call: call, Message: result.Message, Result: result.Result, LogResult: result.LogResult, Error: result.Error, ResultStatus: result.ResultStatus, ExecutionStarted: result.ExecutionStarted}
}

func actorForView(ctx context.Context, view toolshare.Context) contextinfo.Actor {
	if view.Actor.Role != "" {
		return view.Actor
	}
	if actor, ok := contextinfo.ActorFromContext(ctx); ok {
		return actor
	}
	if permission, ok := security.PermissionFromContext(ctx); ok && permission != nil {
		return permission.Actor(view.Scope.ActorID, view.Scope.Platform, view.Scope.ActorID, "")
	}
	return contextinfo.Actor{ID: view.Scope.ActorID, Platform: view.Scope.Platform, PlatformUserID: view.Scope.ActorID, Role: contextinfo.RoleUser}
}

func permissionForManager(ctx context.Context) *security.Permission {
	if permission, ok := security.PermissionFromContext(ctx); ok && permission != nil {
		return permission
	}
	return security.DefaultPermission()
}

func executionError(call llm.ToolCallRequest, message llm.LLMMessage, err error, resultStatus events.ResultStatus) toolshare.ExecutionResult {
	content := fmt.Sprintf("tool call %s failed: %v", call.Name, err)
	message.Segments = llm.TextSegments(content)
	return toolshare.ExecutionResult{Call: call, Message: message, Error: err, ResultStatus: resultStatus, LogResult: events.LogResult{ResultStatus: resultStatus, Content: toolshare.TruncateLogContent(content, 1024)}}
}
