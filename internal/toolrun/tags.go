package toolrun

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
	"elbot/internal/tool"
)

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
		tools := uniqueNames(entry.Tools)
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

// TagPrompt is display material; the caller owns placement in its prompt.
type TagPrompt struct {
	Tag    string
	Prompt string
}

func (s *PreloadService) TagPrompts(ctx context.Context, tags []string) ([]TagPrompt, error) {
	if s == nil || len(tags) == 0 {
		return nil, nil
	}
	cfg, err := s.tags.load(ctx)
	if err != nil {
		return nil, err
	}
	var result []TagPrompt
	for _, tag := range tags {
		if entry, ok := cfg.Tags[normalizeToolTag(tag)]; ok && strings.TrimSpace(entry.Prompt) != "" {
			result = append(result, TagPrompt{Tag: tag, Prompt: entry.Prompt})
		}
	}
	return result, nil
}

// ToolNamesByTag uses the same configured and built-in tags for discovery and completion.
func (s *PreloadService) ToolNamesByTag(ctx context.Context, tag string, allowed func(tool.Tool) bool) []string {
	if s == nil || s.registry == nil {
		return nil
	}
	names := s.registry.NamesByTag(tag, allowed)
	for _, name := range s.tags.configuredToolNamesByTag(ctx, tag) {
		candidate, ok := s.registry.Get(name)
		if ok && (allowed == nil || allowed(candidate)) {
			names = append(names, name)
		}
	}
	return uniqueNames(names)
}

func (s *PreloadService) Tags(ctx context.Context) []string {
	if s == nil || s.registry == nil {
		return nil
	}
	tags := append(s.registry.Tags(), s.tags.configuredTags(ctx)...)
	var result []string
	for _, tag := range uniqueNames(tags) {
		if len(s.ToolNamesByTag(ctx, tag, func(candidate tool.Tool) bool { return canPreloadTool(ctx, candidate) })) > 0 {
			result = append(result, tag)
		}
	}
	return result
}
