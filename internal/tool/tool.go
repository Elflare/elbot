package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/security"
)

type Source string

const (
	SourceBuiltin    Source = "builtin"
	SourceSkillAgent Source = "skill_agent"
	SourceSkillGo    Source = "skill_go"
)

type RiskLevel = security.RiskLevel

const (
	RiskSafe     = security.RiskSafe
	RiskLow      = security.RiskLow
	RiskMedium   = security.RiskMedium
	RiskHigh     = security.RiskHigh
	RiskCritical = security.RiskCritical
)

type Info struct {
	Name           string
	Description    string
	Source         Source
	Risk           RiskLevel
	SuperadminOnly bool
	// Hidden controls prompt/list exposure only. It is not a security boundary.
	Hidden bool
	// OwnerScoped marks tools that only read/write the calling actor's own data.
	// When true, Policy.CanUseTool allows regular users regardless of risk level.
	// The tool handler is responsible for scoping access by actor.
	OwnerScoped bool
	// ForegroundOnly marks tools that are only available in foreground sessions.
	ForegroundOnly bool
	// Tags are user-facing grouping labels for completion and manual preloading.
	// They are not a security boundary and are not exposed through discover_tool.
	Tags      []string
	DependsOn []string
}

type Tool interface {
	Name() string
	Info() Info
	Schema() llm.ToolSchema
	Call(ctx context.Context, req CallRequest) (*Result, error)
}

type RiskAssessment struct {
	Level   RiskLevel
	Reasons []string
}

type RiskAssessor interface {
	AssessRisk(ctx context.Context, req CallRequest) (RiskAssessment, error)
}

// RiskDetailProvider lets a tool render human-readable arguments for risk confirmation.
// It only affects user-facing confirmation detail text; tool execution results stay unchanged.
type RiskDetailProvider interface {
	RiskDetail(ctx context.Context, req CallRequest) (string, error)
}

// ConfirmationPreflightProvider lets a tool validate arguments and target state before user confirmation.
// Returning an error prevents both confirmation and execution for that tool call.
type ConfirmationPreflightProvider interface {
	PreflightConfirmation(ctx context.Context, req CallRequest) error
}

func AssessRisk(ctx context.Context, tool Tool, req CallRequest) (RiskAssessment, error) {
	if assessor, ok := tool.(RiskAssessor); ok {
		assessment, err := assessor.AssessRisk(ctx, req)
		assessment.Level = normalizeRisk(assessment.Level, tool.Info().Risk)
		return assessment, err
	}
	return RiskAssessment{Level: normalizeRisk(tool.Info().Risk, RiskHigh)}, nil
}

type CallRequest struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

type Result struct {
	// Content is the text returned to the LLM for normal tool calls.
	Content string
	// Segments is the multimodal result returned to the LLM. Tools must use typed
	// segments for images/files instead of relying on URL guessing.
	Segments []llm.MessageSegment
	// Warnings are appended to the LLM-visible tool result to nudge future tool use.
	Warnings []string
	// Data is reserved for discover_tool's DiscoveryResult, which Agent uses to
	// inject discovered schemas as top-level tools. Normal tool call results must
	// use Content or Segments instead.
	Data     json.RawMessage
	Metadata map[string]any
	Outputs  []delivery.Output
}

func (r *Result) LLMSegments() []llm.MessageSegment {
	if r == nil {
		return nil
	}
	if len(r.Segments) > 0 {
		return llm.AppendSegmentText(r.Segments, warningsSuffix(r.Warnings))
	}
	return llm.TextSegments(AppendWarnings(r.Content, r.Warnings))
}

func AppendWarnings(content string, warnings []string) string {
	warningText := FormatWarnings(warnings)
	if warningText == "" {
		return content
	}
	content = strings.TrimRight(content, "\n")
	if strings.TrimSpace(content) == "" {
		return warningText
	}
	return content + "\n\n" + warningText
}

func FormatWarnings(warnings []string) string {
	warnings = normalizeWarnings(warnings)
	if len(warnings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Warnings:\n")
	for _, warning := range warnings {
		b.WriteString("- ")
		b.WriteString(strings.ReplaceAll(warning, "\n", "\n  "))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func warningsSuffix(warnings []string) string {
	warningText := FormatWarnings(warnings)
	if warningText == "" {
		return ""
	}
	return "\n\n" + warningText
}

func normalizeWarnings(warnings []string) []string {
	out := make([]string, 0, len(warnings))
	seen := map[string]bool{}
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" || seen[warning] {
			continue
		}
		seen[warning] = true
		out = append(out, warning)
	}
	return out
}

type DiscoveryResult struct {
	Tools  []DiscoveredTool `json:"tools,omitempty"`
	Errors []DiscoveryError `json:"errors,omitempty"`
}

type DiscoveryError struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type PublicInfo struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	Source         string `json:"source"`
	ForegroundOnly bool   `json:"foreground_only,omitempty"`
}

type DiscoveredTool struct {
	Info        PublicInfo      `json:"info"`
	Schema      *llm.ToolSchema `json:"schema,omitempty"`
	Detail      string          `json:"detail,omitempty"`
	DetailBlock DetailBlock     `json:"-"`
}

type DetailBlock struct {
	Content  string
	Format   string
	RuleCard string
}

type DetailProvider interface {
	Detail() string
	ActivateTools() []string
}

type StructuredDetailProvider interface {
	DetailBlock() DetailBlock
}

const MetadataActivateTools = "activate_tools"

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(tool Tool) error {
	if tool == nil {
		return fmt.Errorf("tool is nil")
	}
	name := strings.TrimSpace(tool.Name())
	if name == "" {
		return fmt.Errorf("tool name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool %q already registered", name)
	}
	r.tools[name] = tool
	return nil
}

func (r *Registry) Unregister(name string) error {
	name = strings.TrimSpace(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	tool, ok := r.tools[name]
	if !ok {
		return fmt.Errorf("tool %q not found", name)
	}
	if tool.Info().Source == SourceBuiltin {
		return fmt.Errorf("cannot uninstall builtin tool %q", name)
	}
	delete(r.tools, name)
	return nil
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[strings.TrimSpace(name)]
	return tool, ok
}

func (r *Registry) List() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	infos := make([]Info, 0, len(r.tools))
	for _, tool := range r.tools {
		infos = append(infos, tool.Info())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

func (r *Registry) Schemas() []llm.ToolSchema {
	return r.SchemasForContext(nil)
}

func (r *Registry) SchemasForContext(allowed func(Info) bool) []llm.ToolSchema {
	if tool, ok := r.Get("discover_tool"); ok {
		if allowed == nil || allowed(tool.Info()) {
			return []llm.ToolSchema{tool.Schema()}
		}
	}
	return nil
}

func (r *Registry) ToolNames() []string {
	infos := r.List()
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		if info.Name != "discover_tool" && !info.Hidden {
			names = append(names, info.Name)
		}
	}
	return names
}

func (r *Registry) Tags() []string {
	seen := map[string]bool{}
	for _, info := range r.List() {
		for _, tag := range info.Tags {
			tag = normalizeTag(tag)
			if tag != "" {
				seen[tag] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for tag := range seen {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

func (r *Registry) NamesByTag(tag string, allowed func(Tool) bool) []string {
	tag = normalizeTag(tag)
	if tag == "" {
		return nil
	}
	if allowed == nil {
		allowed = func(Tool) bool { return true }
	}
	names := []string{}
	for _, info := range r.List() {
		candidate, ok := r.Get(info.Name)
		if !ok || !allowed(candidate) || !hasTag(info.Tags, tag) {
			continue
		}
		names = append(names, info.Name)
	}
	return names
}

func CanAccessTool(actor security.Actor, policy *security.Policy, info Info) bool {
	if info.SuperadminOnly && actor.Role != security.RoleSuperadmin {
		return false
	}
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	return policy.CanUseTool(actor, normalizeRisk(info.Risk, RiskHigh), info.OwnerScoped)
}

func (r *Registry) Discover(name string) (*DiscoveryResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		infos := r.List()
		out := make([]DiscoveredTool, 0, len(infos))
		for _, info := range infos {
			if !info.Hidden {
				out = append(out, DiscoveredTool{Info: publicInfo(info)})
			}
		}
		return &DiscoveryResult{Tools: out}, nil
	}
	details, errors := r.DiscoverDetails([]string{name}, func(Tool) bool { return true })
	if len(errors) > 0 {
		return nil, fmt.Errorf("tool %q not found", name)
	}
	return &DiscoveryResult{Tools: details}, nil
}

func (r *Registry) DiscoverDetails(names []string, allowed func(Tool) bool) ([]DiscoveredTool, []DiscoveryError) {
	if allowed == nil {
		allowed = func(Tool) bool { return true }
	}
	seen := map[string]bool{}
	visiting := map[string]bool{}
	details := []DiscoveredTool{}
	errors := []DiscoveryError{}
	for _, name := range normalizeNames(names) {
		details, errors = r.addDiscoveryDetail(name, true, allowed, seen, visiting, details, errors)
	}
	return details, errors
}

func (r *Registry) addDiscoveryDetail(name string, root bool, allowed func(Tool) bool, seen, visiting map[string]bool, details []DiscoveredTool, errors []DiscoveryError) ([]DiscoveredTool, []DiscoveryError) {
	name = strings.TrimSpace(name)
	if name == "" || seen[name] {
		return details, errors
	}
	if visiting[name] {
		return details, errors
	}
	tool, ok := r.Get(name)
	if !ok || !allowed(tool) || (root && tool.Info().Hidden) {
		if root {
			errors = append(errors, DiscoveryError{Name: name, Reason: "not found or not allowed"})
		}
		return details, errors
	}
	visiting[name] = true
	seen[name] = true
	info := tool.Info()
	discovered := DiscoveredTool{Info: publicInfo(info)}
	if detailer, ok := tool.(DetailProvider); ok {
		if structured, ok := tool.(StructuredDetailProvider); ok {
			discovered.DetailBlock = structured.DetailBlock()
			discovered.Detail = RenderDetailBlocks([]DetailBlock{discovered.DetailBlock})
		} else {
			discovered.Detail = detailer.Detail()
			discovered.DetailBlock = DetailBlock{Content: discovered.Detail}
		}
	} else {
		schema := tool.Schema()
		discovered.Schema = &schema
	}
	details = append(details, discovered)
	for _, dep := range info.DependsOn {
		details, errors = r.addDiscoveryDetail(dep, false, allowed, seen, visiting, details, errors)
	}
	delete(visiting, name)
	return details, errors
}

func normalizeNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func normalizeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = normalizeTag(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

func normalizeTag(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return ""
		}
	}
	return tag
}

func hasTag(tags []string, want string) bool {
	want = normalizeTag(want)
	if want == "" {
		return false
	}
	for _, tag := range tags {
		if normalizeTag(tag) == want {
			return true
		}
	}
	return false
}

func publicInfo(info Info) PublicInfo {
	return PublicInfo{Name: info.Name, Description: info.Description, Source: string(info.Source), ForegroundOnly: info.ForegroundOnly}
}

func normalizeRisk(value, fallback RiskLevel) RiskLevel {
	if value != "" {
		return value
	}
	if fallback != "" {
		return fallback
	}
	return RiskHigh
}
