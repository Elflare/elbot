package toolrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"elbot/internal/config"
	globalevents "elbot/internal/events"
	"elbot/internal/tool"
)

type PreloadOptions struct {
	Registry *tool.Registry
	TagsPath string
	Tags     config.ToolTagsConfig
}

// PreloadService prepares tools and display material without writing session
// state. The caller combines one input's updates and commits via StateService.
// Actor, policy, workspace and background restrictions come from the call context.
type PreloadService struct {
	registry *tool.Registry
	tags     *toolTagConfigSource
}

func NewPreloadService(opts PreloadOptions) *PreloadService {
	return &PreloadService{registry: opts.Registry, tags: newToolTagConfigSource(opts.TagsPath, opts.Tags)}
}

type ToolPreload struct {
	Update   StateUpdate
	Accepted []bool
	Invalid  []string
	Content  string
}

type SkillPreload struct {
	Update   StateUpdate
	Accepted []bool
	Names    []string
	Invalid  []string
	Content  string
}

type BackgroundPreload struct {
	Update      StateUpdate
	Skills      []string
	SkillPrompt string
}

func (s *PreloadService) PrepareTools(ctx context.Context, names []string) (ToolPreload, error) {
	result := ToolPreload{Accepted: make([]bool, len(names))}
	if s == nil || s.registry == nil {
		return result, nil
	}
	seen := map[string]bool{}
	var content []string
	for i, name := range names {
		discovery, tag := s.discoverDirective(ctx, name)
		if discovery == nil || len(discovery.Tools) == 0 {
			result.Invalid = append(result.Invalid, name)
			continue
		}
		text, err := s.discoveryContent(ctx, discovery, seen)
		if err != nil {
			return ToolPreload{}, err
		}
		if strings.TrimSpace(text) != "" {
			content = append(content, text)
		}
		result.Update.Tools = append(result.Update.Tools, NativeCachedToolsFromDiscovery(discovery)...)
		if tag != "" {
			result.Update.Tags = append(result.Update.Tags, tag)
		}
		result.Accepted[i] = true
	}
	result.Content = strings.Join(content, "\n\n")
	return result, nil
}

func (s *PreloadService) PrepareSkills(ctx context.Context, sessionID string, names []string) SkillPreload {
	result := SkillPreload{Accepted: make([]bool, len(names))}
	if s == nil || s.registry == nil {
		return result
	}
	seen := map[string]bool{}
	var blocks []tool.DetailBlock
	for i, name := range names {
		name = strings.TrimSpace(name)
		candidate, ok := s.registry.Get(name)
		if !ok || !canPreloadSkill(ctx, candidate) {
			result.Invalid = append(result.Invalid, name)
			continue
		}
		detailer := candidate.(tool.DetailProvider)
		if !seen[name] {
			block, err := skillDetailBlock(ctx, candidate, detailer)
			if err != nil {
				result.Invalid = append(result.Invalid, name)
				level := slog.LevelError
				if errors.Is(err, context.Canceled) {
					level = slog.LevelInfo
				}
				_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
					Category: globalevents.LogAudit,
					Level:    level,
					Name:     "skill_preload_failed",
					Module:   "tool",
					Summary:  "skill_preload_failed",
					Fields:   []slog.Attr{slog.Any("session_id", sessionID), slog.Any("tool", name), slog.Any("error", err)},
				})
				continue
			}
			seen[name] = true
			result.Names = append(result.Names, name)
			blocks = append(blocks, block)
		}
		for _, wrapper := range detailer.ActivateTools() {
			result.Update.Tools = append(result.Update.Tools, s.preloadWrapper(ctx, sessionID, wrapper, false)...)
		}
		result.Accepted[i] = true
	}
	result.Content = tool.RenderDetailBlocksWithContext(ctx, blocks)
	result.Update.ShownRuleCardFormats = tool.NewRuleCardFormatsFromContext(ctx)
	return result
}

// BackgroundSelection resolves task selectors before dependency expansion. The
// same roots are used by Elnis authorization and by the actual task preload.
type BackgroundSelection struct {
	Names []string
	Tag   string
}

func (s *PreloadService) BackgroundSelections(ctx context.Context, names []string) []BackgroundSelection {
	var selections []BackgroundSelection
	for _, name := range uniqueNames(names) {
		if name == "discover_tool" {
			continue
		}
		if s == nil || s.registry == nil {
			selections = append(selections, BackgroundSelection{Names: []string{name}})
			continue
		}
		if _, ok := s.registry.Get(name); ok {
			selections = append(selections, BackgroundSelection{Names: []string{name}})
			continue
		}
		tag := normalizeToolTag(name)
		roots := s.ToolNamesByTag(ctx, tag, func(t tool.Tool) bool {
			// Selection expands authorization roots before a model is chosen.
			// Actual preload separately enforces current API availability.
			info := t.Info()
			_, skill := t.(tool.DetailProvider)
			return !info.ForegroundOnly && !info.Hidden && !skill && t.Name() != "workspace" && tool.CanAccessTool(actorForView(ctx, Context{}), policyForManager(ctx, nil), info)
		})
		if len(roots) == 0 {
			roots, tag = []string{name}, ""
		}
		selections = append(selections, BackgroundSelection{Names: roots, Tag: tag})
	}
	return selections
}

func (s *PreloadService) PrepareBackground(ctx context.Context, sessionID string, selectors, allowed []string) BackgroundPreload {
	var result BackgroundPreload
	if s == nil || s.registry == nil {
		return result
	}
	seen := map[string]bool{}
	var names []string
	tags := map[string][]string{}
	for _, selection := range s.BackgroundSelections(ctx, selectors) {
		for _, name := range selection.Names {
			if allowed != nil && !slices.Contains(allowed, name) {
				_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
					Category: globalevents.LogAudit,
					Level:    slog.LevelInfo,
					Name:     "background_preload_skipped",
					Module:   "tool",
					Summary:  "background_preload_skipped",
					Fields:   []slog.Attr{slog.Any("session_id", sessionID), slog.Any("name", name), slog.Any("reason", "not_authorized")},
				})
				continue
			}
			names = append(names, name)
			if selection.Tag != "" {
				tags[name] = append(tags[name], selection.Tag)
			}
		}
	}
	var sections []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || name == "discover_tool" || seen[name] {
			continue
		}
		seen[name] = true
		candidate, ok := s.registry.Get(name)
		if !ok || !preloadAllowed(ctx, candidate) {
			_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
				Category: globalevents.LogAudit,
				Level:    slog.LevelInfo,
				Name:     "background_preload_skipped",
				Module:   "tool",
				Summary:  "background_preload_skipped",
				Fields:   []slog.Attr{slog.Any("session_id", sessionID), slog.Any("name", name), slog.Any("reason", "not_found_or_not_allowed")},
			})
			continue
		}
		if detailer, ok := candidate.(tool.DetailProvider); ok {
			if candidate.Info().Hidden {
				_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
					Category: globalevents.LogAudit,
					Level:    slog.LevelInfo,
					Name:     "background_preload_skipped",
					Module:   "tool",
					Summary:  "background_preload_skipped",
					Fields:   []slog.Attr{slog.Any("session_id", sessionID), slog.Any("name", name), slog.Any("reason", "hidden_skill")},
				})
				continue
			}
			block, err := skillDetailBlock(ctx, candidate, detailer)
			if err != nil {
				_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
					Category: globalevents.LogAudit,
					Level:    slog.LevelInfo,
					Name:     "background_preload_skipped",
					Module:   "tool",
					Summary:  "background_preload_skipped",
					Fields:   []slog.Attr{slog.Any("session_id", sessionID), slog.Any("name", name), slog.Any("reason", "skill_detail_failed"), slog.Any("error", err)},
				})
				continue
			}
			detail := strings.TrimSpace(tool.RenderDetailBlocks([]tool.DetailBlock{block}))
			if detail == "" {
				_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
					Category: globalevents.LogAudit,
					Level:    slog.LevelInfo,
					Name:     "background_preload_skipped",
					Module:   "tool",
					Summary:  "background_preload_skipped",
					Fields:   []slog.Attr{slog.Any("session_id", sessionID), slog.Any("name", name), slog.Any("reason", "empty_skill_detail")},
				})
				continue
			}
			result.Skills = append(result.Skills, name)
			result.Update.Tags = append(result.Update.Tags, tags[name]...)
			sections = append(sections, "## Skill: "+name+"\n\n"+detail)
			for _, wrapper := range detailer.ActivateTools() {
				result.Update.Tools = append(result.Update.Tools, s.preloadWrapper(ctx, sessionID, wrapper, true)...)
			}
			continue
		}
		if candidate.Info().Hidden {
			_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
				Category: globalevents.LogAudit,
				Level:    slog.LevelInfo,
				Name:     "background_preload_skipped",
				Module:   "tool",
				Summary:  "background_preload_skipped",
				Fields:   []slog.Attr{slog.Any("session_id", sessionID), slog.Any("name", name), slog.Any("reason", "not_found_or_not_allowed")},
			})
			continue
		}
		tools := NativeCachedToolsFromDiscovery(s.discoverNames(ctx, []string{name}))
		if len(tools) > 0 {
			result.Update.Tools = append(result.Update.Tools, tools...)
			result.Update.Tags = append(result.Update.Tags, tags[name]...)
		}
	}
	result.SkillPrompt = strings.Join(sections, "\n\n---\n\n")
	return result
}

func preloadAllowed(ctx context.Context, candidate tool.Tool) bool {
	return tool.InfoAvailableInContext(ctx, candidate.Info()) && tool.CanAccessTool(actorForView(ctx, Context{}), policyForManager(ctx, nil), candidate.Info())
}

func canPreloadTool(ctx context.Context, candidate tool.Tool) bool {
	info := candidate.Info()
	_, skill := candidate.(tool.DetailProvider)
	return info.Name != "discover_tool" && !info.Hidden && !skill && preloadAllowed(ctx, candidate)
}

func canPreloadSkill(ctx context.Context, candidate tool.Tool) bool {
	_, skill := candidate.(tool.DetailProvider)
	return skill && !candidate.Info().Hidden && preloadAllowed(ctx, candidate)
}

func (s *PreloadService) discoverDirective(ctx context.Context, name string) (*tool.DiscoveryResult, string) {
	name = strings.TrimSpace(name)
	if candidate, ok := s.registry.Get(name); ok {
		if !canPreloadTool(ctx, candidate) {
			return nil, ""
		}
		return s.discoverNames(ctx, []string{name}), ""
	}
	tag := normalizeToolTag(name)
	names := s.ToolNamesByTag(ctx, tag, func(candidate tool.Tool) bool { return canPreloadTool(ctx, candidate) })
	return s.discoverNames(ctx, names), tag
}

func (s *PreloadService) discoverNames(ctx context.Context, names []string) *tool.DiscoveryResult {
	details, _ := s.registry.DiscoverDetails(ctx, names, func(candidate tool.Tool) bool { return preloadAllowed(ctx, candidate) })
	result := &tool.DiscoveryResult{}
	for _, item := range details {
		if item.Schema != nil && item.Info.Name != "" && item.Detail == "" {
			result.Tools = append(result.Tools, item)
		}
	}
	return result
}

func (s *PreloadService) discoveryContent(ctx context.Context, discovery *tool.DiscoveryResult, seen map[string]bool) (string, error) {
	var parts []string
	for _, item := range discovery.Tools {
		name := strings.TrimSpace(item.Info.Name)
		if item.Schema == nil || name == "" || seen[name] {
			continue
		}
		candidate, ok := s.registry.Get(name)
		if !ok {
			continue
		}
		if _, ok := candidate.(tool.ContextDiscoveryContentProvider); !ok {
			continue
		}
		seen[name] = true
		content, _, _, err := tool.LoadDiscoveryContent(ctx, candidate)
		if err != nil {
			return "", fmt.Errorf("load discovery content for %s: %w", name, err)
		}
		if strings.TrimSpace(content) != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func skillDetailBlock(ctx context.Context, candidate tool.Tool, detailer tool.DetailProvider) (tool.DetailBlock, error) {
	if loader, ok := candidate.(tool.LazyDetailProvider); ok {
		return loader.LoadDetail(ctx)
	}
	if structured, ok := candidate.(tool.StructuredDetailProvider); ok {
		return structured.DetailBlock(), nil
	}
	return tool.DetailBlock{Content: detailer.Detail()}, nil
}

func (s *PreloadService) preloadWrapper(ctx context.Context, sessionID, name string, background bool) []CachedTool {
	name = strings.TrimSpace(name)
	if name == "" || name == "discover_tool" {
		return nil
	}
	eventName, key := "skill_wrapper_preload_skipped", "tool"
	if background {
		eventName, key = "background_preload_skipped", "name"
	}
	skip := func(reason string) {
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category:     globalevents.LogAudit,
			Level:        slog.LevelInfo,
			Name:         eventName,
			Module:       "tool",
			Summary:      eventName,
			ResultStatus: globalevents.ResultSkipped,
			Fields:       slog.Group("", "session_id", sessionID, key, name, "reason", reason).Value.Group(),
		})
	}
	candidate, ok := s.registry.Get(name)
	if !ok || !preloadAllowed(ctx, candidate) {
		skip("not_found_or_not_allowed")
		return nil
	}
	if _, skill := candidate.(tool.DetailProvider); skill {
		skip("skill_has_no_schema")
		return nil
	}
	if background && !candidate.Info().Hidden {
		return NativeCachedToolsFromDiscovery(s.discoverNames(ctx, []string{name}))
	}
	schema, info := candidate.Schema(), candidate.Info()
	if schema.Name == "" {
		skip("empty_schema")
		return nil
	}
	return []CachedTool{{Name: info.Name, Source: SourceKindNative, Description: info.Description, Schema: cloneSchema(schema), ForegroundOnly: info.ForegroundOnly}}
}
