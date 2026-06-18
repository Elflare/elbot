package agent

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"

	"elbot/internal/config"
	"elbot/internal/security"
	"elbot/internal/tool"
)

const systemPromptPriorityToolTagPrompts = 200

type toolTagConfigSource struct {
	path  string
	mu    sync.Mutex
	cache toolTagConfigCache
}

type toolTagConfigCache struct {
	loaded bool
	config config.ToolTagsConfig
	state  toolTagFileState
}

type toolTagFileState struct {
	size    int64
	modTime time.Time
}

func newToolTagConfigSource(path string, initial config.ToolTagsConfig) *toolTagConfigSource {
	return &toolTagConfigSource{path: strings.TrimSpace(path), cache: toolTagConfigCache{loaded: path == "", config: normalizeToolTagsConfig(initial)}}
}

func (s *toolTagConfigSource) Parts(ctx context.Context, req SystemPromptRequest) ([]SystemPromptPart, error) {
	if s == nil || req.Session == nil {
		return nil, nil
	}
	metadata := decodeSessionMetadata(req.Session.Metadata)
	if len(metadata.ToolTags) == 0 {
		return nil, nil
	}
	cfg, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	parts := []SystemPromptPart{}
	for _, tag := range metadata.ToolTags {
		entry, ok := cfg.Tags[normalizeToolTag(tag)]
		if !ok || strings.TrimSpace(entry.Prompt) == "" {
			continue
		}
		parts = append(parts, SystemPromptPart{Name: "tool_tag_prompt:" + tag, Priority: systemPromptPriorityToolTagPrompts, Content: entry.Prompt})
	}
	return parts, nil
}

func (s *toolTagConfigSource) configuredTags(ctx context.Context) []string {
	cfg, err := s.load(ctx)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Tags))
	for tag := range cfg.Tags {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

func (s *toolTagConfigSource) configuredTagsForTool(ctx context.Context, name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	cfg, err := s.load(ctx)
	if err != nil {
		return nil
	}
	out := []string{}
	for tag, entry := range cfg.Tags {
		for _, toolName := range entry.Tools {
			if toolName == name {
				out = append(out, tag)
				break
			}
		}
	}
	return sortedUnique(out)
}

func (s *toolTagConfigSource) configuredToolNamesByTag(ctx context.Context, tag string) []string {
	tag = normalizeToolTag(tag)
	if tag == "" {
		return nil
	}
	cfg, err := s.load(ctx)
	if err != nil {
		return nil
	}
	return append([]string(nil), cfg.Tags[tag].Tools...)
}

func (s *toolTagConfigSource) load(ctx context.Context) (config.ToolTagsConfig, error) {
	if s == nil {
		return config.ToolTagsConfig{}, nil
	}
	if err := ctx.Err(); err != nil {
		return config.ToolTagsConfig{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return s.cache.config, nil
	}
	state, err := currentToolTagFileState(s.path)
	if err != nil {
		return config.ToolTagsConfig{}, err
	}
	if s.cache.loaded && sameToolTagFileState(s.cache.state, state) {
		return s.cache.config, nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := config.ToolTagsConfig{}
			s.cache = toolTagConfigCache{loaded: true, config: cfg, state: state}
			return cfg, nil
		}
		return config.ToolTagsConfig{}, fmt.Errorf("read tool tags config %q: %w", s.path, err)
	}
	var cfg config.ToolTagsConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return config.ToolTagsConfig{}, fmt.Errorf("parse tool tags config %q: %w", s.path, err)
	}
	cfg = normalizeToolTagsConfig(cfg)
	s.cache = toolTagConfigCache{loaded: true, config: cfg, state: state}
	return cfg, nil
}

func currentToolTagFileState(path string) (toolTagFileState, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return toolTagFileState{}, nil
		}
		return toolTagFileState{}, fmt.Errorf("stat tool tags config %q: %w", path, err)
	}
	return toolTagFileState{size: info.Size(), modTime: info.ModTime()}, nil
}

func sameToolTagFileState(left, right toolTagFileState) bool {
	return left.size == right.size && left.modTime.Equal(right.modTime)
}

func normalizeToolTagsConfig(cfg config.ToolTagsConfig) config.ToolTagsConfig {
	out := config.ToolTagsConfig{Tags: map[string]config.ToolTagConfig{}}
	for tag, entry := range cfg.Tags {
		tag = normalizeToolTag(tag)
		if tag == "" {
			continue
		}
		tools := sortedUnique(entry.Tools)
		if len(tools) == 0 && strings.TrimSpace(entry.Prompt) == "" {
			continue
		}
		out.Tags[tag] = config.ToolTagConfig{Tools: tools, Prompt: strings.TrimSpace(entry.Prompt)}
	}
	if len(out.Tags) == 0 {
		out.Tags = nil
	}
	return out
}

func normalizeToolTag(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return ""
		}
	}
	return tag
}

func (a *Agent) namesByToolTag(ctx context.Context, tag string, allowed func(tool.Tool) bool) []string {
	if a.toolRuntime.registry == nil {
		return nil
	}
	names := a.toolRuntime.registry.NamesByTag(tag, allowed)
	if a.toolRuntime.toolTags != nil {
		for _, name := range a.toolRuntime.toolTags.configuredToolNamesByTag(ctx, tag) {
			candidate, ok := a.toolRuntime.registry.Get(name)
			if !ok || allowed != nil && !allowed(candidate) {
				continue
			}
			names = append(names, name)
		}
	}
	return sortedUnique(names)
}

func (a *Agent) completionToolTags(ctx context.Context, registry *tool.Registry, actor security.Actor, policy *security.Policy) []string {
	if registry == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, tag := range registry.Tags() {
		if len(a.completionToolNamesByTag(ctx, registry, tag, func(candidate tool.Tool) bool { return a.canPreloadToolRoot(actor, policy, candidate) })) > 0 {
			seen[tag] = true
		}
	}
	if a.toolRuntime.toolTags != nil {
		for _, tag := range a.toolRuntime.toolTags.configuredTags(ctx) {
			if len(a.completionToolNamesByTag(ctx, registry, tag, func(candidate tool.Tool) bool { return a.canPreloadToolRoot(actor, policy, candidate) })) > 0 {
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

func (a *Agent) completionToolNamesByTag(ctx context.Context, registry *tool.Registry, tag string, allowed func(tool.Tool) bool) []string {
	if registry == nil {
		return nil
	}
	names := registry.NamesByTag(tag, allowed)
	if a.toolRuntime.toolTags != nil {
		for _, name := range a.toolRuntime.toolTags.configuredToolNamesByTag(ctx, tag) {
			candidate, ok := registry.Get(name)
			if !ok || allowed != nil && !allowed(candidate) {
				continue
			}
			names = append(names, name)
		}
	}
	return sortedUnique(names)
}
