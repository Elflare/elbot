package toolrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/elyph"
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
	// NeedsElyphRules is merged with other material before preparing the input.
	NeedsElyphRules bool
	Update          StateUpdate
	Accepted        []bool
	Invalid         []string
	Content         string
}

type SkillPreload struct {
	// NeedsElyphRules reflects the actual formats of loaded skills.
	NeedsElyphRules bool
	Update          StateUpdate
	Accepted        []bool
	Names           []string
	Invalid         []string
	Content         string
}

type BackgroundPreload struct {
	// NeedsElyphRules covers both preloaded tools and skills.
	NeedsElyphRules bool
	Update          StateUpdate
	Skills          []string
	SkillPrompt     string
}

func (s *PreloadService) PrepareTools(ctx context.Context, names []string) (ToolPreload, error) {
	result := ToolPreload{Accepted: make([]bool, len(names))}
	seen := map[string]bool{}
	var content []string
	for i, name := range names {
		discovery, tag, err := s.discoverDirective(ctx, name)
		if err != nil {
			return ToolPreload{}, err
		}
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
		for _, item := range discovery.Tools {
			result.NeedsElyphRules = result.NeedsElyphRules || item.NeedsElyphRules
		}
		if tag != "" {
			result.Update.Tags = append(result.Update.Tags, tag)
		}
		result.Accepted[i] = true
	}
	result.Content = strings.Join(content, "\n\n")
	return result, nil
}

func (s *PreloadService) PrepareSkills(ctx context.Context, sessionID string, names []string) (SkillPreload, error) {
	record := globalevents.LogRecord{Category: globalevents.LogAudit, Module: "tool", Identity: globalevents.LogIdentity{SessionID: &sessionID}}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if facts.RunID != "" {
		record.Identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		record.Identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		record.Identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		record.Identity.RootRequestID = &facts.RootRequestID
	}

	result := SkillPreload{Accepted: make([]bool, len(names))}
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
				record.EventName = "skill_preload_failed"
				record.Level = level
				record.ResultStatus = globalevents.ResultFailed
				if errors.Is(err, context.Canceled) {
					record.ResultStatus = globalevents.ResultCanceled
				}
				record.Content = fmt.Sprintf("skill preload failed: tool=%s error=%v", name, err)
				_ = globalevents.EmitLog(ctx, record)
				return SkillPreload{}, err
			}
			seen[name] = true
			result.Names = append(result.Names, name)
			blocks = append(blocks, block)
			result.NeedsElyphRules = result.NeedsElyphRules || (block.Format == elyph.Format && strings.TrimSpace(block.Content) != "")
		}
		for _, wrapper := range detailer.ActivateTools() {
			result.Update.Tools = append(result.Update.Tools, s.preloadWrapper(ctx, sessionID, wrapper, false)...)
		}
		result.Accepted[i] = true
	}
	result.Content = tool.RenderDetailBlocks(blocks)
	return result, nil
}

// BackgroundSelection resolves task selectors before dependency expansion. The
// same roots are used by Elnis authorization and by the actual task preload.
type BackgroundSelection struct {
	Names []string
	Tag   string
}

func (s *PreloadService) BackgroundSelections(ctx context.Context, names []string) ([]BackgroundSelection, error) {
	var selections []BackgroundSelection
	for _, name := range uniqueNames(names) {
		if name == "discover_tool" {
			continue
		}
		if _, ok := s.registry.Get(name); ok {
			selections = append(selections, BackgroundSelection{Names: []string{name}})
			continue
		}
		tag := normalizeToolTag(name)
		roots, err := s.ToolNamesByTag(ctx, tag, func(t tool.Tool) bool {
			// Selection expands authorization roots before a model is chosen.
			// Actual preload separately enforces current API availability.
			info := t.Info()
			_, skill := t.(tool.DetailProvider)
			return !info.ForegroundOnly && !info.Hidden && !skill && t.Name() != "workspace" && tool.CanAccessTool(actorForView(ctx, Context{}), policyForManager(ctx), info)
		})
		if err != nil {
			return nil, err
		}
		if len(roots) == 0 {
			roots, tag = []string{name}, ""
		}
		selections = append(selections, BackgroundSelection{Names: roots, Tag: tag})
	}
	return selections, nil
}

func (s *PreloadService) PrepareBackground(ctx context.Context, sessionID string, selectors, allowed []string) (BackgroundPreload, error) {
	record := globalevents.LogRecord{Category: globalevents.LogAudit, Module: "tool", Identity: globalevents.LogIdentity{SessionID: &sessionID}}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if facts.RunID != "" {
		record.Identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		record.Identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		record.Identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		record.Identity.RootRequestID = &facts.RootRequestID
	}

	var result BackgroundPreload
	seen := map[string]bool{}
	var names []string
	tags := map[string][]string{}
	selections, err := s.BackgroundSelections(ctx, selectors)
	if err != nil {
		return BackgroundPreload{}, err
	}
	for _, selection := range selections {
		for _, name := range selection.Names {
			if allowed != nil && !slices.Contains(allowed, name) {
				record.EventName = "background_preload_skipped"
				record.Level = slog.LevelInfo
				record.ResultStatus = globalevents.ResultSkipped
				record.Content = fmt.Sprintf("background preload skipped: name=%s reason=not_authorized", name)
				_ = globalevents.EmitLog(ctx, record)
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
			record.EventName = "background_preload_skipped"
			record.Level = slog.LevelInfo
			record.ResultStatus = globalevents.ResultSkipped
			record.Content = fmt.Sprintf("background preload skipped: name=%s reason=not_found_or_not_allowed", name)
			_ = globalevents.EmitLog(ctx, record)
			continue
		}
		if detailer, ok := candidate.(tool.DetailProvider); ok {
			if candidate.Info().Hidden {
				record.EventName = "background_preload_skipped"
				record.Level = slog.LevelInfo
				record.ResultStatus = globalevents.ResultSkipped
				record.Content = fmt.Sprintf("background preload skipped: name=%s reason=hidden_skill", name)
				_ = globalevents.EmitLog(ctx, record)
				continue
			}
			block, err := skillDetailBlock(ctx, candidate, detailer)
			if err != nil {
				record.EventName = "background_preload_skipped"
				record.Level = slog.LevelError
				record.ResultStatus = globalevents.ResultFailed
				if errors.Is(err, context.Canceled) {
					record.Level = slog.LevelInfo
					record.ResultStatus = globalevents.ResultCanceled
				}
				record.Content = fmt.Sprintf("background preload failed: name=%s error=%v", name, err)
				_ = globalevents.EmitLog(ctx, record)
				return BackgroundPreload{}, err
			}
			detail := strings.TrimSpace(tool.RenderDetailBlocks([]tool.DetailBlock{block}))
			if detail == "" {
				record.EventName = "background_preload_skipped"
				record.Level = slog.LevelInfo
				record.ResultStatus = globalevents.ResultSkipped
				record.Content = fmt.Sprintf("background preload skipped: name=%s reason=empty_skill_detail", name)
				_ = globalevents.EmitLog(ctx, record)
				continue
			}
			result.Skills = append(result.Skills, name)
			result.NeedsElyphRules = result.NeedsElyphRules || block.Format == elyph.Format
			result.Update.Tags = append(result.Update.Tags, tags[name]...)
			sections = append(sections, "## Skill: "+name+"\n\n"+detail)
			for _, wrapper := range detailer.ActivateTools() {
				result.Update.Tools = append(result.Update.Tools, s.preloadWrapper(ctx, sessionID, wrapper, true)...)
			}
			continue
		}
		if candidate.Info().Hidden {
			record.EventName = "background_preload_skipped"
			record.Level = slog.LevelInfo
			record.ResultStatus = globalevents.ResultSkipped
			record.Content = fmt.Sprintf("background preload skipped: name=%s reason=not_found_or_not_allowed", name)
			_ = globalevents.EmitLog(ctx, record)
			continue
		}
		discovery := s.discoverNames(ctx, []string{name})
		tools := NativeCachedToolsFromDiscovery(discovery)
		for _, item := range discovery.Tools {
			result.NeedsElyphRules = result.NeedsElyphRules || item.NeedsElyphRules
		}
		if len(tools) > 0 {
			result.Update.Tools = append(result.Update.Tools, tools...)
			result.Update.Tags = append(result.Update.Tags, tags[name]...)
		}
	}
	result.SkillPrompt = strings.Join(sections, "\n\n---\n\n")
	return result, nil
}

func preloadAllowed(ctx context.Context, candidate tool.Tool) bool {
	return tool.InfoAvailableInContext(ctx, candidate.Info()) && tool.CanAccessTool(actorForView(ctx, Context{}), policyForManager(ctx), candidate.Info())
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

func (s *PreloadService) discoverDirective(ctx context.Context, name string) (*tool.DiscoveryResult, string, error) {
	name = strings.TrimSpace(name)
	if candidate, ok := s.registry.Get(name); ok {
		if !canPreloadTool(ctx, candidate) {
			return nil, "", nil
		}
		return s.discoverNames(ctx, []string{name}), "", nil
	}
	tag := normalizeToolTag(name)
	names, err := s.ToolNamesByTag(ctx, tag, func(candidate tool.Tool) bool { return canPreloadTool(ctx, candidate) })
	if err != nil {
		return nil, "", err
	}
	return s.discoverNames(ctx, names), tag, nil
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
	record := globalevents.LogRecord{Category: globalevents.LogAudit, Module: "tool", Identity: globalevents.LogIdentity{SessionID: &sessionID}}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if facts.RunID != "" {
		record.Identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		record.Identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		record.Identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		record.Identity.RootRequestID = &facts.RootRequestID
	}

	name = strings.TrimSpace(name)
	if name == "" || name == "discover_tool" {
		return nil
	}
	eventName, key := "skill_wrapper_preload_skipped", "tool"
	if background {
		eventName, key = "background_preload_skipped", "name"
	}
	skip := func(reason string) {
		record.EventName = eventName
		record.Level = slog.LevelInfo
		record.ResultStatus = globalevents.ResultSkipped
		record.Content = fmt.Sprintf("%s: %s=%s reason=%s", eventName, key, name, reason)
		_ = globalevents.EmitLog(ctx, record)
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
