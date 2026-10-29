// 解析工具执行来源，调度原生与 Elwisp 调用。
package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/security"
	"elbot/internal/storage"
)

// ManagerDeps 由根部一次交付执行所需能力，不持有兄弟组件。
type ManagerDeps struct {
	Registry interface {
		Get(string) (toolshare.Tool, bool)
		ToolInfo(string) (toolshare.Info, bool)
		QueryTools(context.Context, toolshare.ToolQuery) ([]toolshare.Tool, error)
	} // 工具解析与目录查询。
	Permission interface {
		CheckTool(contextinfo.Actor, string, security.RiskLevel, bool, bool, string) security.ToolPermissionResult
	} // 当前调用的授权与确认判定。
	Media interface {
		Metadata(context.Context, string) (toolshare.MediaInfo, error)
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
	// Source identifies the selected provider; empty means no provider was found.
	Source toolshare.SourceKind
	// Native is the selected native implementation, nil for remote or unavailable tools.
	Native toolshare.Tool
	// Discovered 是本次选中的已发现工具声明；直接解析的前台工具没有此声明。
	Discovered *toolshare.DiscoveredTool
	// ResultStatus is declared by resolve: succeeded, rejected by context, or failed lookup/configuration.
	ResultStatus events.ResultStatus
	// Error retains the resolution error, including typed permission denial; nil does not declare success.
	Error error
}

func NewManager(deps ManagerDeps) *Manager { return &Manager{ManagerDeps: deps} }

func (m *Manager) resolve(ctx context.Context, name string, discovered []toolshare.DiscoveredTool, mode string) resolvedTool {
	name = strings.TrimSpace(name)
	if name == "" {
		return resolvedTool{ResultStatus: events.ResultFailed, Error: fmt.Errorf("tool name is empty")}
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
		discoveredCopy := discoveredTool
		if mode != storage.SessionModeBackground && !m.InfoAvailableInContext(ctx, toolshare.Info{ForegroundOnly: discoveredCopy.ForegroundOnly}) {
			return resolvedTool{Source: discoveredCopy.Source, Discovered: &discoveredCopy, ResultStatus: events.ResultRejected, Error: toolshare.PolicyDeniedError("tool is unavailable in this context")}
		}
		source := discoveredCopy.Source
		if source == toolshare.SourceKindELwisp {
			if discoveredCopy.Endpoint == "" {
				return resolvedTool{Source: source, Discovered: &discoveredCopy, ResultStatus: events.ResultFailed, Error: fmt.Errorf("ELwisp tool endpoint is missing")}
			}
			return resolvedTool{Source: source, Discovered: &discoveredCopy, ResultStatus: events.ResultSucceeded}
		}
		if nativeTool, ok := m.Registry.Get(discoveredCopy.Name); ok {
			if reason := m.UnavailableReason(ctx, nativeTool.Info()); reason != "" {
				return resolvedTool{Source: toolshare.SourceKindNative, Native: nativeTool, Discovered: &discoveredCopy, ResultStatus: events.ResultRejected, Error: toolshare.PolicyDeniedError(reason)}
			}
			return resolvedTool{Source: toolshare.SourceKindNative, Native: nativeTool, Discovered: &discoveredCopy, ResultStatus: events.ResultSucceeded}
		}
		return resolvedTool{Source: toolshare.SourceKindNative, Discovered: &discoveredCopy, ResultStatus: events.ResultFailed, Error: fmt.Errorf("native tool is no longer available")}
	}
	if mode == storage.SessionModeBackground {
		return resolvedTool{ResultStatus: events.ResultRejected, Error: toolshare.PolicyDeniedError("tool is not allowed in this background task")}
	}
	if nativeTool, ok := m.Registry.Get(name); ok {
		if reason := m.UnavailableReason(ctx, nativeTool.Info()); reason != "" {
			return resolvedTool{Source: toolshare.SourceKindNative, Native: nativeTool, ResultStatus: events.ResultRejected, Error: toolshare.PolicyDeniedError(reason)}
		}
		return resolvedTool{Source: toolshare.SourceKindNative, Native: nativeTool, ResultStatus: events.ResultSucceeded}
	}
	return resolvedTool{ResultStatus: events.ResultFailed, Error: fmt.Errorf("tool %q not found", name)}
}

// CallTool 供已由调用方授权的原生调用使用；仍先准备，再执行同一结果。
// 此入口不进入模型批次或交互确认。
func (m *Manager) CallTool(ctx context.Context, req toolshare.CallRequest) (*toolshare.Result, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	candidate, ok := m.Registry.Get(req.Name)
	if !ok {
		return nil, "", fmt.Errorf("tool %q not found", req.Name)
	}
	prepared, err := candidate.PrepareCall(ctx, req)
	if err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if !prepared.Matches(req) {
		return nil, "", fmt.Errorf("tool call changed after preparation")
	}
	return prepared.Call(ctx)
}
