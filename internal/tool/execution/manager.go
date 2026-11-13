// 解析工具执行来源，调度原生与 Elwisp 调用。
package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	"elbot/internal/security"
	"elbot/internal/storage"
)

// ManagerDeps 由根部一次交付执行所需能力，不持有兄弟组件。
type ManagerDeps struct {
	Registry interface {
		Get(string) (toolshare.ToolDefinition, bool)
		Query(context.Context, toolshare.ToolQuery) ([]toolshare.ToolDefinition, error)
	} // 工具解析与目录查询。
	Permission interface {
		CheckTool(contextinfo.Actor, string, security.RiskLevel, bool, bool, bool) security.ToolPermissionResult
	} // 当前调用的授权与确认判定。
	Media interface {
		Metadata(context.Context, string) (operationresult.Result[toolshare.MediaInfo], error)
	} // 参数引用的媒体查询。
	Log                       func(context.Context, events.LogRecord)                                 // 执行记录发布。
	InfoAvailableInContext    func(context.Context, toolshare.Info) bool                              // schema 上下文资格。
	UnavailableReason         func(context.Context, toolshare.Info) string                            // 解析拒绝原因。
	BackgroundDiscoveredTools func([]toolshare.DiscoveredTool) []toolshare.DiscoveredTool             // 后台声明筛选。
	DecodeState               func(string) (toolshare.State, error)                                   // 会话工具状态解码。
	PrepareUpdate             func(string, toolshare.StateUpdate) (storage.SessionMetadata, error)    // 随结果原子提交的元数据补丁。
	DiscoveryStateUpdate      func(context.Context, *toolshare.Result) (toolshare.StateUpdate, error) // 本次发现或激活结果。
}

type Manager struct{ ManagerDeps }

type resolvedTool struct {
	Source     toolshare.SourceKind
	Native     *toolshare.ToolDefinition
	Discovered *toolshare.DiscoveredTool
}

func NewManager(deps ManagerDeps) *Manager { return &Manager{ManagerDeps: deps} }

func (m *Manager) resolve(ctx context.Context, name string, discovered []toolshare.DiscoveredTool, mode string) (operationresult.Result[resolvedTool], error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return operationresult.Result[resolvedTool]{Status: operationresult.InvalidArguments, Message: "tool name is empty"}, nil
	}
	for _, discoveredTool := range discovered {
		modelName := discoveredTool.CanonicalName
		if modelName == "" {
			modelName = discoveredTool.Name
		}
		if modelName != name {
			continue
		}
		if mode == storage.SessionModeBackground && len(m.BackgroundDiscoveredTools([]toolshare.DiscoveredTool{discoveredTool})) == 0 {
			continue
		}
		if mode != storage.SessionModeBackground && !m.InfoAvailableInContext(ctx, toolshare.Info{ForegroundOnly: discoveredTool.ForegroundOnly}) {
			return operationresult.Result[resolvedTool]{Status: operationresult.Forbidden, Message: "tool is unavailable in this context"}, nil
		}
		source := discoveredTool.Source
		if source == toolshare.SourceKindELwisp {
			if discoveredTool.Endpoint == "" {
				return operationresult.Result[resolvedTool]{}, fmt.Errorf("ELwisp tool endpoint is missing")
			}
			return operationresult.Result[resolvedTool]{Status: operationresult.Success, Data: resolvedTool{Source: source, Discovered: &discoveredTool}}, nil
		}
		if nativeTool, ok := m.Registry.Get(discoveredTool.Name); ok {
			if reason := m.UnavailableReason(ctx, nativeTool.Definition); reason != "" {
				return operationresult.Result[resolvedTool]{Status: operationresult.Forbidden, Message: reason}, nil
			}
			return operationresult.Result[resolvedTool]{Status: operationresult.Success, Data: resolvedTool{Source: toolshare.SourceKindNative, Native: &nativeTool, Discovered: &discoveredTool}}, nil
		}
		return operationresult.Result[resolvedTool]{Status: operationresult.Unavailable, Message: "native tool is no longer available"}, nil
	}
	if mode == storage.SessionModeBackground {
		return operationresult.Result[resolvedTool]{Status: operationresult.Forbidden, Message: "tool is not allowed in this background task"}, nil
	}
	if nativeTool, ok := m.Registry.Get(name); ok {
		if reason := m.UnavailableReason(ctx, nativeTool.Definition); reason != "" {
			return operationresult.Result[resolvedTool]{Status: operationresult.Forbidden, Message: reason}, nil
		}
		return operationresult.Result[resolvedTool]{Status: operationresult.Success, Data: resolvedTool{Source: toolshare.SourceKindNative, Native: &nativeTool}}, nil
	}
	return operationresult.Result[resolvedTool]{Status: operationresult.NotFound, Message: fmt.Sprintf("tool %q not found", name)}, nil
}

// CallTool 供已由调用方授权的原生调用使用；仍先准备，再执行同一结果。
// 此入口不进入模型批次或交互确认。
func (m *Manager) CallTool(ctx context.Context, req toolshare.CallRequest) (operationresult.Result[*toolshare.Result], string, error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*toolshare.Result]{}, "Tool execution not entered: " + err.Error(), err
	}
	candidate, ok := m.Registry.Get(req.Name)
	if !ok {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.NotFound, Message: fmt.Sprintf("tool %q not found", req.Name)}, "", nil
	}
	prepared, err := candidate.PrepareCall(ctx, req)
	if err != nil {
		return operationresult.Result[*toolshare.Result]{}, "Tool execution not entered: " + err.Error(), err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[*toolshare.Result]{Status: prepared.Status, Message: prepared.Message}, prepared.Message, nil
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*toolshare.Result]{}, "Tool execution not entered: " + err.Error(), err
	}
	preparedCall := prepared.Data
	if !preparedCall.Matches(req) {
		err := fmt.Errorf("tool call changed after preparation")
		return operationresult.Result[*toolshare.Result]{}, "Tool execution not entered: " + err.Error(), err
	}
	return preparedCall.Call(ctx)
}
