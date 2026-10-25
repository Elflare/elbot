// 解析工具执行来源，调度原生与 Elwisp 调用。
package execution

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/events"
	"elbot/internal/media"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/registry"
)

type Manager struct {
	registry   *registry.Registry                      // 本次调用解析使用的原生工具目录。
	permission *security.Permission                    // 根部注入的调用权限与确认策略。
	media      *media.Manager                          // 执行前查询工具参数引用的媒体。
	log        func(context.Context, events.LogRecord) // Root-owned publication for complete execution records.
}

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

func NewManager(registry *registry.Registry, permission *security.Permission, media *media.Manager, log func(context.Context, events.LogRecord)) *Manager {
	return &Manager{registry: registry, permission: permission, media: media, log: log}
}

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
		if mode == storage.SessionModeBackground && len(registry.BackgroundDiscoveredTools(ctx, []toolshare.DiscoveredTool{discoveredTool})) == 0 {
			continue
		}
		discoveredCopy := discoveredTool
		if mode != storage.SessionModeBackground && !registry.InfoAvailableInContext(ctx, toolshare.Info{ForegroundOnly: discoveredCopy.ForegroundOnly}) {
			return resolvedTool{Source: discoveredCopy.Source, Discovered: &discoveredCopy, ResultStatus: events.ResultRejected, Error: toolshare.PolicyDeniedError("tool is unavailable in this context")}
		}
		source := discoveredCopy.Source
		if source == toolshare.SourceKindELwisp {
			if discoveredCopy.Endpoint == "" {
				return resolvedTool{Source: source, Discovered: &discoveredCopy, ResultStatus: events.ResultFailed, Error: fmt.Errorf("ELwisp tool endpoint is missing")}
			}
			return resolvedTool{Source: source, Discovered: &discoveredCopy, ResultStatus: events.ResultSucceeded}
		}
		if nativeTool, ok := m.registry.Get(discoveredCopy.Name); ok {
			if reason := registry.UnavailableReason(ctx, nativeTool.Info()); reason != "" {
				return resolvedTool{Source: toolshare.SourceKindNative, Native: nativeTool, Discovered: &discoveredCopy, ResultStatus: events.ResultRejected, Error: toolshare.PolicyDeniedError(reason)}
			}
			return resolvedTool{Source: toolshare.SourceKindNative, Native: nativeTool, Discovered: &discoveredCopy, ResultStatus: events.ResultSucceeded}
		}
		return resolvedTool{Source: toolshare.SourceKindNative, Discovered: &discoveredCopy, ResultStatus: events.ResultFailed, Error: fmt.Errorf("native tool is no longer available")}
	}
	if mode == storage.SessionModeBackground {
		return resolvedTool{ResultStatus: events.ResultRejected, Error: toolshare.PolicyDeniedError("tool is not allowed in this background task")}
	}
	if nativeTool, ok := m.registry.Get(name); ok {
		if reason := registry.UnavailableReason(ctx, nativeTool.Info()); reason != "" {
			return resolvedTool{Source: toolshare.SourceKindNative, Native: nativeTool, ResultStatus: events.ResultRejected, Error: toolshare.PolicyDeniedError(reason)}
		}
		return resolvedTool{Source: toolshare.SourceKindNative, Native: nativeTool, ResultStatus: events.ResultSucceeded}
	}
	return resolvedTool{ResultStatus: events.ResultFailed, Error: fmt.Errorf("tool %q not found", name)}
}

// CallTool 供已由调用方授权的原生调用使用，不进入模型批次或交互确认。
func (m *Manager) CallTool(ctx context.Context, req toolshare.CallRequest) (*toolshare.Result, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	candidate, ok := m.registry.Get(req.Name)
	if !ok {
		return nil, "", fmt.Errorf("tool %q not found", req.Name)
	}
	return candidate.Call(ctx, req)
}
