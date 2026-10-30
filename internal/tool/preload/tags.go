// 读取工具标签配置，提供标签查询和提示内容。
package preload

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"

	"elbot/internal/config"
	"elbot/internal/tool/share"
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

func (s *toolTagConfigSource) configuredTags(ctx context.Context) ([]string, error) {
	cfg, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cfg.Tags))
	for tag := range cfg.Tags {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out, nil
}

func (s *PreloadService) namesByTag(ctx context.Context, tag string) ([]string, error) {
	tag = normalizeToolTag(tag)
	if tag == "" {
		return []string{}, nil
	}
	cfg, err := s.tags.load(ctx)
	if err != nil {
		return nil, err
	}
	return uniqueNames(append(s.Registry.NamesByTag(tag), cfg.Tags[tag].Tools...)), nil
}

func (s *toolTagConfigSource) load(ctx context.Context) (config.ToolTagsConfig, error) {
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
	if s.cache.loaded && s.cache.state.size == state.size && s.cache.state.modTime.Equal(state.modTime) {
		return s.cache.config, nil
	}
	data, err := os.ReadFile(s.path)
	var cfg config.ToolTagsConfig
	if err == nil {
		if err := toml.Unmarshal(data, &cfg); err != nil {
			return config.ToolTagsConfig{}, fmt.Errorf("parse tool tags config %q: %w", s.path, err)
		}
	} else if !os.IsNotExist(err) {
		return config.ToolTagsConfig{}, fmt.Errorf("read tool tags config %q: %w", s.path, err)
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

func (s *PreloadService) TagPrompts(ctx context.Context, tags []string) ([]share.TagPrompt, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	cfg, err := s.tags.load(ctx)
	if err != nil {
		return nil, err
	}
	var result []share.TagPrompt
	for _, tag := range tags {
		if entry := cfg.Tags[normalizeToolTag(tag)]; entry.Prompt != "" {
			result = append(result, share.TagPrompt{Tag: tag, Prompt: entry.Prompt})
		}
	}
	return result, nil
}

// ToolNamesByTag uses the same configured and built-in tags for discovery and completion.
func (s *PreloadService) ToolNamesByTag(ctx context.Context, tag string, query share.ToolQuery) ([]string, error) {
	names, err := s.namesByTag(ctx, tag)
	if err != nil {
		return nil, err
	}
	if query.Names != nil {
		names = slices.DeleteFunc(names, func(name string) bool { return !slices.Contains(query.Names, name) })
	}
	query.Names = names
	infos, err := s.Registry.QueryTools(ctx, query)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(infos))
	for _, info := range infos {
		result = append(result, info.Name())
	}
	return result, nil
}

func (s *PreloadService) Tags(ctx context.Context) ([]string, error) {
	configured, err := s.tags.configuredTags(ctx)
	if err != nil {
		return nil, err
	}
	tags := append(s.Registry.Tags(), configured...)
	var result []string
	for _, tag := range uniqueNames(tags) {
		names, err := s.ToolNamesByTag(ctx, tag, share.ToolQuery{Kind: "tool"})
		if err != nil {
			return nil, err
		}
		if len(names) > 0 {
			result = append(result, tag)
		}
	}
	return result, nil
}

func uniqueNames(names []string) []string {
	result := make([]string, 0, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return slices.Compact(result)
}
