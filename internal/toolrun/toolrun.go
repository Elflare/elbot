package toolrun

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"elbot/internal/llm"
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
}

type Context struct {
	Mode             string
	Session          *storage.Session
	Scope            session.Scope
	Actor            security.Actor
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
	Name      string
	Source    SourceKind
	Native    tool.Tool
	Cached    *CachedTool
	Available bool
	Reason    string
}

type ExecutionResult struct {
	Call    llm.ToolCallRequest
	Message llm.LLMMessage
	Result  *tool.Result
	Err     error
}

func NewManager(registry *tool.Registry, policy *security.Policy) *Manager {
	return &Manager{Native: NewNativeSource(registry), Policy: policy}
}

func (m *Manager) ToolInfos(ctx context.Context, view Context) ([]tool.Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if view.DisableBaseTools || view.Mode != storage.SessionModeWork || m == nil || m.Native == nil {
		return nil, nil
	}
	return m.Native.ToolInfos(ctx, actorForView(ctx, view), policyForManager(ctx, m.Policy)), nil
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
	if view.DisableBaseTools || view.Mode != storage.SessionModeWork || m == nil || m.Native == nil {
		return nil, nil
	}
	return m.Native.BaseSchemas(ctx), nil
}

func (m *Manager) Schemas(ctx context.Context, view Context, cached []CachedTool) ([]llm.ToolSchema, error) {
	base, err := m.BaseSchemas(ctx, view)
	if err != nil {
		return nil, err
	}
	out := make([]llm.ToolSchema, 0, len(base)+len(cached))
	seen := map[string]bool{}
	appendSchema := func(schema llm.ToolSchema) {
		name := schema.Function.Name
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, schemaForContext(ctx, schema))
	}
	for _, schema := range base {
		appendSchema(schema)
	}
	for _, cachedTool := range cached {
		if !cachedToolAvailable(ctx, cachedTool) {
			continue
		}
		appendSchema(cachedTool.Schema)
	}
	return out, nil
}

func schemaForContext(ctx context.Context, schema llm.ToolSchema) llm.ToolSchema {
	sandbox, ok := tool.SandboxContextFromContext(ctx)
	if !ok || !sandbox.Background || !backgroundPathSchema(schema.Function.Name) {
		return schema
	}
	schema.Function.Description = strings.TrimSpace(schema.Function.Description + " " + tool.BackgroundPathInstruction())
	return schema
}

func backgroundPathSchema(name string) bool {
	switch name {
	case "shell", "read_file", "edit_file", "send_file":
		return true
	default:
		return false
	}
}

func cachedToolAvailable(ctx context.Context, cached CachedTool) bool {
	return AvailableInContext(ctx, tool.Info{ForegroundOnly: cached.ForegroundOnly})
}

func (m *Manager) Resolve(ctx context.Context, name string, cached []CachedTool) ResolvedTool {
	name = strings.TrimSpace(name)
	if name == "" {
		return ResolvedTool{Name: name, Available: false, Reason: "tool name is empty"}
	}
	for _, cachedTool := range cached {
		if cachedTool.Name != name && cachedTool.CanonicalName != name {
			continue
		}
		cachedCopy := cachedTool
		if !cachedToolAvailable(ctx, cachedCopy) {
			return ResolvedTool{Name: name, Source: cachedCopy.Source, Cached: &cachedCopy, Available: false, Reason: "tool is unavailable in this context"}
		}
		source := cachedCopy.Source
		if source == "" {
			source = SourceKindNative
		}
		if source == SourceKindELwisp {
			if cachedCopy.Endpoint == "" {
				return ResolvedTool{Name: name, Source: source, Cached: &cachedCopy, Available: false, Reason: "ELwisp tool endpoint is missing"}
			}
			return ResolvedTool{Name: cachedCopy.CanonicalName, Source: source, Cached: &cachedCopy, Available: true}
		}
		if m != nil && m.Native != nil {
			if nativeTool, ok := m.Native.Get(cachedCopy.Name); ok {
				return ResolvedTool{Name: cachedCopy.Name, Source: SourceKindNative, Native: nativeTool, Cached: &cachedCopy, Available: true}
			}
		}
		return ResolvedTool{Name: cachedCopy.Name, Source: SourceKindNative, Cached: &cachedCopy, Available: false, Reason: "native tool is no longer available"}
	}
	if m != nil && m.Native != nil {
		if nativeTool, ok := m.Native.Get(name); ok {
			if !AvailableInContext(ctx, nativeTool.Info()) {
				return ResolvedTool{Name: name, Source: SourceKindNative, Native: nativeTool, Available: false, Reason: unavailableReason(ctx, nativeTool.Info())}
			}
			return ResolvedTool{Name: name, Source: SourceKindNative, Native: nativeTool, Available: true}
		}
	}
	return ResolvedTool{Name: name, Available: false, Reason: fmt.Sprintf("tool %q not found", name)}
}

func (m *Manager) AssessRisk(ctx context.Context, resolved ResolvedTool, arguments string) (tool.RiskAssessment, error) {
	if resolved.Source == SourceKindELwisp {
		return tool.RiskAssessment{Level: tool.RiskLow}, nil
	}
	if !resolved.Available || resolved.Native == nil {
		return tool.RiskAssessment{Level: tool.RiskLow}, nil
	}
	return tool.AssessRisk(ctx, resolved.Native, tool.CallRequest{Name: resolved.Name, Arguments: json.RawMessage(arguments)})
}

func (m *Manager) PreflightConfirmation(ctx context.Context, resolved ResolvedTool, call llm.ToolCallRequest) error {
	if resolved.Source == SourceKindELwisp || !resolved.Available || resolved.Native == nil {
		return nil
	}
	provider, ok := resolved.Native.(tool.ConfirmationPreflightProvider)
	if !ok {
		return nil
	}
	return provider.PreflightConfirmation(ctx, tool.CallRequest{ID: call.ID, Name: resolved.Name, Arguments: json.RawMessage(call.Arguments)})
}

func (m *Manager) RiskDetail(ctx context.Context, resolved ResolvedTool, call llm.ToolCallRequest) string {
	if resolved.Source == SourceKindELwisp || !resolved.Available || resolved.Native == nil {
		return ""
	}
	provider, ok := resolved.Native.(tool.RiskDetailProvider)
	if !ok {
		return ""
	}
	detail, err := provider.RiskDetail(ctx, tool.CallRequest{ID: call.ID, Name: resolved.Name, Arguments: json.RawMessage(call.Arguments)})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(detail)
}

func (m *Manager) Execute(ctx context.Context, call llm.ToolCallRequest, resolved ResolvedTool, actor security.Actor) ExecutionResult {
	message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID}
	if !resolved.Available {
		return executionError(call, message, fmt.Errorf("%s", resolved.Reason))
	}
	if resolved.Source == SourceKindELwisp {
		return executeELwispTool(ctx, call, resolved)
	}
	registry := (*tool.Registry)(nil)
	if m != nil && m.Native != nil {
		registry = m.Native.Registry
	}
	result := tool.Executor{Registry: registry, Actor: actor, Policy: policyForManager(ctx, m.Policy)}.Execute(ctx, call)
	return ExecutionResult{Call: result.Call, Message: result.Message, Result: result.Result, Err: result.Err}
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
		out = append(out, CachedTool{Name: discovered.Info.Name, Source: SourceKindNative, Description: discovered.Info.Description, Schema: *discovered.Schema, ForegroundOnly: discovered.Info.ForegroundOnly})
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

func actorForView(ctx context.Context, view Context) security.Actor {
	if view.Actor.Role != "" {
		return view.Actor
	}
	if actor, ok := security.ActorFromContext(ctx); ok {
		return actor
	}
	if policy, ok := security.PolicyFromContext(ctx); ok && policy != nil {
		return policy.Actor(view.Scope.ActorID, view.Scope.Platform, view.Scope.ActorID, "")
	}
	return security.Actor{ID: view.Scope.ActorID, Platform: view.Scope.Platform, PlatformUserID: view.Scope.ActorID, Role: security.RoleUser}
}

func policyForManager(ctx context.Context, configured *security.Policy) *security.Policy {
	if configured != nil {
		return configured
	}
	if policy, ok := security.PolicyFromContext(ctx); ok && policy != nil {
		return policy
	}
	return security.DefaultPolicy()
}

func executionError(call llm.ToolCallRequest, message llm.LLMMessage, err error) ExecutionResult {
	content := fmt.Sprintf("tool call %s failed: %v", call.Name, err)
	message.Segments = llm.TextSegments(content)
	return ExecutionResult{Call: call, Message: message, Err: err}
}
