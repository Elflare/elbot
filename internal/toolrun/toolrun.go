package toolrun

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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

type SourceKind string

const (
	SourceKindNative SourceKind = "native"
	SourceKindELwisp SourceKind = "elwisp"
)

type Manager struct {
	Native *NativeSource
	Policy *security.Policy
	Media  *media.Manager
}

type Context struct {
	Mode             string
	Session          *storage.Session
	Scope            session.Scope
	Actor            contextinfo.Actor
	DisableBaseTools bool
}

type CachedTool struct {
	Name           string         `json:"name"`
	CanonicalName  string         `json:"canonical_name,omitempty"`
	Source         SourceKind     `json:"source"`
	Description    string         `json:"description,omitempty"`
	Schema         llm.ToolSchema `json:"schema"`
	ELwispName     string         `json:"elwisp_name,omitempty"`
	EventKey       string         `json:"event_key,omitempty"`
	Endpoint       string         `json:"endpoint,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	ForegroundOnly bool           `json:"foreground_only,omitempty"`
}

type ResolvedTool struct {
	// Name is the resolved execution name; failed lookup may retain the requested name.
	Name string
	// Source identifies the selected provider; empty means no provider was found.
	Source SourceKind
	// Native is the selected native implementation, nil for remote or unavailable tools.
	Native tool.Tool
	// Cached is the selected discovery metadata snapshot, nil for uncached tools.
	Cached *CachedTool
	// ResultStatus is declared by Resolve: succeeded, rejected by policy, or failed lookup/configuration.
	ResultStatus events.ResultStatus
	// Error retains the resolution error, including typed policy denial; nil does not declare success.
	Error error
}

type ExecutionResult struct {
	// Call is the original request, before native or remote execution.
	Call llm.ToolCallRequest
	// Message is the model-facing result, retaining any partial tool segments.
	Message llm.LLMMessage
	// Result is the raw tool result, which can coexist with Error; nil means none.
	Result *tool.Result
	// LogResult.Content carries the independent tool log body. Before call entry,
	// the execution source declares ResultStatus and the reason; after entry,
	// the runner uses ResultStatus for the invocation log outcome.
	LogResult events.LogResult
	// Error is the original preparation or execution error, not a later output/save error.
	Error error
	// ResultStatus is the explicit result supplied by the native or remote execution source.
	ResultStatus events.ResultStatus
	// ExecutionStarted records entry into a tool call or the remote HTTP send boundary.
	ExecutionStarted bool
}

func NewManager(registry *tool.Registry, policy *security.Policy) *Manager {
	return &Manager{Native: NewNativeSource(registry), Policy: policy}
}

func (m *Manager) ToolInfos(ctx context.Context, view Context) ([]tool.Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if view.DisableBaseTools || view.Mode != storage.SessionModeWork {
		return nil, nil
	}
	return m.Native.ToolInfos(ctx, actorForView(ctx, view), m.Policy), nil
}

func (m *Manager) ToolNames(ctx context.Context, view Context) ([]string, error) {
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

func (m *Manager) BaseSchemas(ctx context.Context, view Context) ([]llm.ToolSchema, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if view.DisableBaseTools || view.Mode != storage.SessionModeWork {
		return nil, nil
	}
	return m.Native.BaseSchemas(ctx), nil
}

func (m *Manager) Schemas(ctx context.Context, view Context, cached []CachedTool) ([]llm.ToolSchema, error) {
	if view.Mode != storage.SessionModeWork && view.Mode != storage.SessionModeBackground {
		return nil, nil
	}
	base, err := m.BaseSchemas(ctx, view)
	if err != nil {
		return nil, err
	}
	out := make([]llm.ToolSchema, 0, len(base)+len(cached))
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
	for _, cachedTool := range cached {
		if view.Mode == storage.SessionModeBackground && !backgroundToolAllowed(ctx, cachedTool) {
			continue
		}
		if !cachedToolAvailable(ctx, cachedTool) {
			continue
		}
		appendSchema(cachedTool.Schema)
	}
	return out, nil
}

func cachedToolAvailable(ctx context.Context, cached CachedTool) bool {
	return AvailableInContext(ctx, tool.Info{ForegroundOnly: cached.ForegroundOnly})
}

func (m *Manager) Resolve(ctx context.Context, name string, cached []CachedTool, mode string) ResolvedTool {
	name = strings.TrimSpace(name)
	if name == "" {
		return ResolvedTool{Name: name, ResultStatus: events.ResultFailed, Error: fmt.Errorf("tool name is empty")}
	}
	for _, cachedTool := range cached {
		if cachedTool.Name != name && cachedTool.CanonicalName != name {
			continue
		}
		if mode == storage.SessionModeBackground && !backgroundToolAllowed(ctx, cachedTool) {
			continue
		}
		cachedCopy := cachedTool
		if !cachedToolAvailable(ctx, cachedCopy) {
			return ResolvedTool{Name: name, Source: cachedCopy.Source, Cached: &cachedCopy, ResultStatus: events.ResultRejected, Error: tool.PolicyDeniedError("tool is unavailable in this context")}
		}
		source := cachedCopy.Source
		if source == SourceKindELwisp {
			if cachedCopy.Endpoint == "" {
				return ResolvedTool{Name: name, Source: source, Cached: &cachedCopy, ResultStatus: events.ResultFailed, Error: fmt.Errorf("ELwisp tool endpoint is missing")}
			}
			return ResolvedTool{Name: cachedCopy.CanonicalName, Source: source, Cached: &cachedCopy, ResultStatus: events.ResultSucceeded}
		}
		if nativeTool, ok := m.Native.Get(cachedCopy.Name); ok {
			return ResolvedTool{Name: cachedCopy.Name, Source: SourceKindNative, Native: nativeTool, Cached: &cachedCopy, ResultStatus: events.ResultSucceeded}
		}
		return ResolvedTool{Name: cachedCopy.Name, Source: SourceKindNative, Cached: &cachedCopy, ResultStatus: events.ResultFailed, Error: fmt.Errorf("native tool is no longer available")}
	}
	if mode == storage.SessionModeBackground {
		return ResolvedTool{Name: name, ResultStatus: events.ResultRejected, Error: tool.PolicyDeniedError("tool is not allowed in this background task")}
	}
	if nativeTool, ok := m.Native.Get(name); ok {
		if !AvailableInContext(ctx, nativeTool.Info()) {
			return ResolvedTool{Name: name, Source: SourceKindNative, Native: nativeTool, ResultStatus: events.ResultRejected, Error: tool.PolicyDeniedError(unavailableReason(ctx, nativeTool.Info()))}
		}
		return ResolvedTool{Name: name, Source: SourceKindNative, Native: nativeTool, ResultStatus: events.ResultSucceeded}
	}
	return ResolvedTool{Name: name, ResultStatus: events.ResultFailed, Error: fmt.Errorf("tool %q not found", name)}
}

func (m *Manager) AssessRisk(ctx context.Context, resolved ResolvedTool, arguments string) (tool.RiskAssessment, error) {
	if resolved.Source == SourceKindELwisp {
		return tool.RiskAssessment{Level: tool.RiskLow}, nil
	}
	return tool.AssessRisk(ctx, resolved.Native, tool.CallRequest{Name: resolved.Name, Arguments: json.RawMessage(arguments)})
}

func (m *Manager) PrepareCall(ctx context.Context, resolved ResolvedTool, call llm.ToolCallRequest) (*tool.PreparedCall, error) {
	if resolved.Source == SourceKindELwisp {
		return nil, nil
	}
	return tool.PrepareCall(ctx, resolved.Native, tool.CallRequest{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
}

func (m *Manager) Execute(ctx context.Context, call llm.ToolCallRequest, resolved ResolvedTool, actor contextinfo.Actor) ExecutionResult {
	return m.ExecutePrepared(ctx, call, resolved, actor, nil, nil)
}

func (m *Manager) ExecutePrepared(ctx context.Context, call llm.ToolCallRequest, resolved ResolvedTool, actor contextinfo.Actor, prepared *tool.PreparedCall, onStarted func()) ExecutionResult {

	message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID}
	if resolved.ResultStatus != events.ResultSucceeded {
		return executionError(call, message, resolved.Error, resolved.ResultStatus)
	}
	if resolved.Source == SourceKindELwisp {
		return executeELwispTool(ctx, call, resolved, onStarted)
	}
	result := tool.Executor{Registry: m.Native.Registry, Actor: actor, Policy: m.Policy, OnStarted: onStarted}.ExecutePrepared(ctx, call, prepared)
	return ExecutionResult{Call: call, Message: result.Message, Result: result.Result, LogResult: result.LogResult, Error: result.Error, ResultStatus: result.ResultStatus, ExecutionStarted: result.ExecutionStarted}
}

func NativeCachedToolsFromDiscovery(result *tool.DiscoveryResult) []CachedTool {
	if result == nil {
		return nil
	}
	out := make([]CachedTool, 0, len(result.Tools))
	for _, discovered := range result.Tools {
		if discovered.Schema == nil || discovered.Info.Name == "" {
			continue
		}
		out = append(out, CachedTool{Name: discovered.Info.Name, Source: SourceKindNative, Description: discovered.Info.Description, Schema: cloneSchema(*discovered.Schema), ForegroundOnly: discovered.Info.ForegroundOnly})
	}
	return out
}

func SortCachedTools(tools []CachedTool) []CachedTool {
	out := append([]CachedTool(nil), tools...)
	sort.Slice(out, func(i, j int) bool {
		left := out[i].CanonicalName
		if left == "" {
			left = out[i].Name
		}
		right := out[j].CanonicalName
		if right == "" {
			right = out[j].Name
		}
		return left < right
	})
	return out
}

func actorForView(ctx context.Context, view Context) contextinfo.Actor {
	if view.Actor.Role != "" {
		return view.Actor
	}
	if actor, ok := contextinfo.ActorFromContext(ctx); ok {
		return actor
	}
	if policy, ok := security.PolicyFromContext(ctx); ok && policy != nil {
		return policy.Actor(view.Scope.ActorID, view.Scope.Platform, view.Scope.ActorID, "")
	}
	return contextinfo.Actor{ID: view.Scope.ActorID, Platform: view.Scope.Platform, PlatformUserID: view.Scope.ActorID, Role: contextinfo.RoleUser}
}

func policyForManager(ctx context.Context) *security.Policy {
	if policy, ok := security.PolicyFromContext(ctx); ok && policy != nil {
		return policy
	}
	return security.DefaultPolicy()
}

func executionError(call llm.ToolCallRequest, message llm.LLMMessage, err error, resultStatus events.ResultStatus) ExecutionResult {
	content := fmt.Sprintf("tool call %s failed: %v", call.Name, err)
	message.Segments = llm.TextSegments(content)
	return ExecutionResult{Call: call, Message: message, Error: err, ResultStatus: resultStatus, LogResult: events.LogResult{ResultStatus: resultStatus, Content: tool.TruncateLogContent(content, 1024)}}
}
