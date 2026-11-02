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

// NamesByTag 基于传入定义展开声明和配置标签，不重新读取目录。
func (s *PreloadService) NamesByTag(ctx context.Context, tag string, definitions []share.ToolDefinition) ([]string, error) {
	tag = share.NormalizeToolTag(tag)
	if tag == "" {
		return []string{}, nil
	}
	cfg, err := s.tags.load(ctx)
	if err != nil {
		return nil, err
	}
	names := append([]string(nil), cfg.Tags[tag].Tools...)
	for _, definition := range definitions {
		if slices.Contains(definition.Definition.Tags, tag) {
			names = append(names, definition.Definition.Name)
		}
	}
	return uniqueNames(names), nil
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
		tag = share.NormalizeToolTag(tag)
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
		if entry := cfg.Tags[share.NormalizeToolTag(tag)]; entry.Prompt != "" {
			result = append(result, share.TagPrompt{Tag: tag, Prompt: entry.Prompt})
		}
	}
	return result, nil
}

func (s *PreloadService) Tags(ctx context.Context) ([]string, error) {
	cfg, err := s.tags.load(ctx)
	if err != nil {
		return nil, err
	}
	definitions, err := s.Registry.Query(ctx, share.ToolQuery{Kind: "tool"})
	if err != nil {
		return nil, err
	}
	tags := []string{}
	for _, definition := range definitions {
		tags = append(tags, definition.Definition.Tags...)
		for tag, entry := range cfg.Tags {
			if slices.Contains(entry.Tools, definition.Definition.Name) {
				tags = append(tags, tag)
			}
		}
	}
	return uniqueNames(tags), nil
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
