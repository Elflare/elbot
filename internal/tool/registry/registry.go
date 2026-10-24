// 管理工具注册、替换、注销和目录查询。
package registry

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"elbot/internal/llm"
	"elbot/internal/security"
	toolshare "elbot/internal/tool/share"
)

// registration 的指针标识一次注册；Tool 保存原对象，查询时不暴露包装。
type registration struct {
	toolshare.Tool // 保留工具原有的动态类型与可选能力。
}

type Registry struct {
	mu         sync.RWMutex             // 保护注册目录及原子替换。
	tools      map[string]*registration // 名称到注册记录，释放时按记录身份核对。
	permission *security.Permission     // 根部注入的统一访问权限策略。
}

func NewRegistry(permission *security.Permission) *Registry {
	return &Registry{tools: map[string]*registration{}, permission: permission}
}

func (r *Registry) Register(tool toolshare.Tool) error {
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
	r.tools[name] = &registration{Tool: tool}
	return nil
}

// RegisterOwned registers one owner's tools atomically and returns an idempotent release.
// The release removes only these exact registrations, including builtin business tools.
func (r *Registry) RegisterOwned(tools ...toolshare.Tool) (func(), error) {
	entries := make(map[string]*registration, len(tools))
	for _, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("tool is nil")
		}
		name := strings.TrimSpace(t.Name())
		if name == "" {
			return nil, fmt.Errorf("tool name is required")
		}
		if _, exists := entries[name]; exists {
			return nil, fmt.Errorf("tool %q already registered", name)
		}
		entries[name] = &registration{Tool: t}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range entries {
		if _, exists := r.tools[name]; exists {
			return nil, fmt.Errorf("tool %q already registered", name)
		}
	}
	for name, t := range entries {
		r.tools[name] = t
	}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for name, t := range entries {
			if r.tools[name] == t {
				delete(r.tools, name)
			}
		}
	}, nil
}

// ReplaceSources atomically replaces every tool owned by the selected sources.
// Existing tools from other sources are preserved and may not be shadowed.
func (r *Registry) ReplaceSources(sources []toolshare.Source, replacements []toolshare.Tool) error {
	sourceSet := make(map[toolshare.Source]bool, len(sources))
	for _, source := range sources {
		if source == toolshare.SourceBuiltin {
			return fmt.Errorf("builtin tools cannot be replaced")
		}
		sourceSet[source] = true
	}
	if len(sourceSet) == 0 {
		return fmt.Errorf("replacement sources are required")
	}

	type candidate struct {
		name string
		tool toolshare.Tool
	}
	candidates := make([]candidate, 0, len(replacements))
	seen := make(map[string]bool, len(replacements))
	problems := []string{}
	for i, replacement := range replacements {
		if replacement == nil {
			problems = append(problems, fmt.Sprintf("replacement[%d] is nil", i))
			continue
		}
		name := strings.TrimSpace(replacement.Name())
		if name == "" {
			problems = append(problems, fmt.Sprintf("replacement[%d] has no name", i))
			continue
		}
		if !sourceSet[replacement.Info().Source] {
			problems = append(problems, fmt.Sprintf("tool %q has source %q outside replacement set", name, replacement.Info().Source))
		}
		if seen[name] {
			problems = append(problems, fmt.Sprintf("tool %q appears more than once", name))
		} else {
			seen[name] = true
		}
		candidates = append(candidates, candidate{name: name, tool: replacement})
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, candidate := range candidates {
		if existing, ok := r.tools[candidate.name]; ok && !sourceSet[existing.Info().Source] {
			problems = append(problems, fmt.Sprintf("tool %q conflicts with existing source %q", candidate.name, existing.Info().Source))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("replace tool sources: %s", strings.Join(problems, "; "))
	}

	next := make(map[string]*registration, len(r.tools)+len(candidates))
	for name, existing := range r.tools {
		if !sourceSet[existing.Info().Source] {
			next[name] = existing
		}
	}
	for _, candidate := range candidates {
		next[candidate.name] = &registration{Tool: candidate.tool}
	}
	r.tools = next
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
	if tool.Info().Source == toolshare.SourceBuiltin {
		return fmt.Errorf("cannot uninstall builtin tool %q", name)
	}
	delete(r.tools, name)
	return nil
}

func (r *Registry) Get(name string) (toolshare.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[strings.TrimSpace(name)]
	if !ok {
		return nil, false
	}
	return tool.Tool, true
}

// ToolInfo 返回目录元数据；不执行上下文资格筛选。
func (r *Registry) ToolInfo(name string) (toolshare.Info, bool) {
	tool, ok := r.Get(name)
	if !ok {
		return toolshare.Info{}, false
	}
	return tool.Info(), true
}

// ToolSchema 读取指定工具的 schema，不决定模型可见的 schema 集合。
func (r *Registry) ToolSchema(name string) (llm.ToolSchema, bool) {
	tool, ok := r.Get(name)
	if !ok {
		return llm.ToolSchema{}, false
	}
	return tool.Schema(), true
}

func (r *Registry) List() []toolshare.Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	infos := make([]toolshare.Info, 0, len(r.tools))
	for _, tool := range r.tools {
		infos = append(infos, tool.Info())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// ToolNames 返回完整目录名称；可见性与权限筛选使用 QueryTools。
func (r *Registry) ToolNames() []string {
	infos := r.List()
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
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

// NamesByTag 只匹配目录标签；调用方通过 QueryTools 统一筛选资格。
func (r *Registry) NamesByTag(tag string) []string {
	tag = normalizeTag(tag)
	if tag == "" {
		return nil
	}
	names := []string{}
	for _, info := range r.List() {
		if !hasTag(info.Tags, tag) {
			continue
		}
		names = append(names, info.Name)
	}
	return names
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
	for _, tag := range tags {
		if normalizeTag(tag) == want {
			return true
		}
	}
	return false
}
