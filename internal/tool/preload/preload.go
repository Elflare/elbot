// 预加载工具和 Skill，生成展示材料与待提交状态。
package preload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/elyph"
	globalevents "elbot/internal/events"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/tool/discovery"
	toolregistry "elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

type PreloadOptions struct {
	Registry *toolregistry.Registry // 已配置统一权限策略的工具目录。
	TagsPath string                 // 标签配置文件路径；为空时使用 Tags。
	Tags     config.ToolTagsConfig  // 不使用配置文件时的标签配置。
}

// PreloadService 准备工具和展示材料，不写会话状态，也不触发 Skill 扫描。
// 调用方提供身份、模型、工作目录和前后台事实；资格筛选统一交给 Registry。
type PreloadService struct {
	registry *toolregistry.Registry // 提供目录事实与已筛选的原工具对象。
	resolver *discovery.Resolver    // 解析选中对象及其依赖，保留本次使用的对象。
	tags     *toolTagConfigSource   // 读取标签成员及提示内容。
}

func NewPreloadService(opts PreloadOptions) *PreloadService {
	return &PreloadService{
		registry: opts.Registry,
		resolver: discovery.NewResolver(opts.Registry),
		tags:     newToolTagConfigSource(opts.TagsPath, opts.Tags),
	}
}

func (s *PreloadService) PrepareTools(ctx context.Context, names []string) (share.ToolPreload, error) {
	if err := ctx.Err(); err != nil {
		return share.ToolPreload{}, err
	}
	result := share.ToolPreload{Accepted: make([]bool, len(names))}
	seen := map[string]bool{}
	var content []string
	for i, name := range names {
		details, targets, tag, err := s.discoverDirective(ctx, name)
		if err != nil {
			return share.ToolPreload{}, err
		}
		if len(details.Tools) == 0 {
			result.Invalid = append(result.Invalid, name)
			continue
		}
		text, err := s.discoveryContent(ctx, details, targets, seen)
		if err != nil {
			return share.ToolPreload{}, err
		}
		if strings.TrimSpace(text) != "" {
			content = append(content, text)
		}
		result.Update.Tools = append(result.Update.Tools, discovery.NativeCachedToolsFromDiscovery(details)...)
		for _, item := range details.Tools {
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

func (s *PreloadService) PrepareSkills(ctx context.Context, sessionID string, names []string) (share.SkillPreload, error) {
	candidates, err := s.registry.QueryTools(ctx, share.ToolQuery{Names: uniqueNames(names), Kind: "skill"})
	if err != nil {
		return share.SkillPreload{}, err
	}
	targets := make(map[string]share.Tool, len(candidates))
	for _, candidate := range candidates {
		targets[candidate.Name()] = candidate
	}
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

	result := share.SkillPreload{Accepted: make([]bool, len(names))}
	seen := map[string]bool{}
	var blocks []share.DetailBlock
	for i, name := range names {
		name = strings.TrimSpace(name)
		candidate, ok := targets[name]
		if !ok {
			result.Invalid = append(result.Invalid, name)
			continue
		}
		detailer := candidate.(share.DetailProvider)
		if !seen[name] {
			block, err := skillDetailBlock(ctx, candidate, detailer)
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
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
				return share.SkillPreload{}, err
			}
			seen[name] = true
			result.Names = append(result.Names, name)
			blocks = append(blocks, block)
			result.NeedsElyphRules = result.NeedsElyphRules || (block.Format == elyph.Format && strings.TrimSpace(block.Content) != "")
			for _, wrapper := range uniqueNames(detailer.ActivateTools()) {
				tools, err := s.preloadWrapper(ctx, sessionID, wrapper, sandboxctx.BackgroundContext(ctx))
				if err != nil {
					return share.SkillPreload{}, err
				}
				result.Update.Tools = append(result.Update.Tools, tools...)
			}
		}
		result.Accepted[i] = true
	}
	result.Content = discovery.RenderDetailBlocks(blocks)
	return result, nil
}

func (s *PreloadService) discoverDirective(ctx context.Context, name string) (*share.DiscoveryResult, map[string]share.Tool, string, error) {
	name = strings.TrimSpace(name)
	names, tag := []string{name}, ""
	if _, exists := s.registry.Get(name); !exists {
		tag = normalizeToolTag(name)
		configured, err := s.tags.configuredToolNamesByTag(ctx, tag)
		if err != nil {
			return nil, nil, "", err
		}
		names = uniqueNames(append(s.registry.NamesByTag(tag), configured...))
	}
	candidates, err := s.registry.QueryTools(ctx, share.ToolQuery{Names: names, Kind: "tool"})
	if err != nil {
		return nil, nil, "", err
	}
	result, targets, err := s.discoverNames(ctx, candidates)
	return result, targets, tag, err
}

func (s *PreloadService) discoverNames(ctx context.Context, candidates []share.Tool) (*share.DiscoveryResult, map[string]share.Tool, error) {
	details, targets, err := s.resolver.Resolve(ctx, candidates)
	if err != nil {
		return nil, nil, err
	}
	result := &share.DiscoveryResult{Errors: details.Errors}
	for _, item := range details.Tools {
		if item.Schema != nil && item.Info.Name != "" && item.Detail == "" {
			result.Tools = append(result.Tools, item)
		}
	}
	return result, targets, nil
}

func (s *PreloadService) discoveryContent(ctx context.Context, details *share.DiscoveryResult, targets map[string]share.Tool, seen map[string]bool) (string, error) {
	var parts []string
	for _, item := range details.Tools {
		name := item.Info.Name
		if seen[name] {
			continue
		}
		candidate := targets[name]
		if _, ok := candidate.(share.ContextDiscoveryContentProvider); !ok {
			continue
		}
		seen[name] = true
		content, _, _, err := discovery.LoadDiscoveryContent(ctx, candidate)
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			return "", fmt.Errorf("load discovery content for %s: %w", name, err)
		}
		if strings.TrimSpace(content) != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func skillDetailBlock(ctx context.Context, candidate share.Tool, detailer share.DetailProvider) (share.DetailBlock, error) {
	if loader, ok := candidate.(share.LazyDetailProvider); ok {
		return loader.LoadDetail(ctx)
	}
	if structured, ok := candidate.(share.StructuredDetailProvider); ok {
		return structured.DetailBlock(), nil
	}
	return share.DetailBlock{Content: detailer.Detail()}, nil
}

func (s *PreloadService) preloadWrapper(ctx context.Context, sessionID, name string, background bool) ([]share.DiscoveredTool, error) {
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
		return nil, nil
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
	candidates, err := s.registry.QueryTools(ctx, share.ToolQuery{Names: []string{name}, IncludeHidden: true})
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		skip("not_found_or_not_allowed")
		return nil, nil
	}
	candidate := candidates[0]
	info := candidate.Info()
	// 可执行 Skill 的文档和自身 schema 分开提供，激活时直接读取所选对象的 schema。
	_, skill := candidate.(share.DetailProvider)
	if background && !info.Hidden && !skill {
		result, _, err := s.discoverNames(ctx, candidates)
		if err != nil {
			return nil, err
		}
		return discovery.NativeCachedToolsFromDiscovery(result), nil
	}
	schema := candidate.Schema()
	if schema.Name == "" {
		skip("empty_schema")
		return nil, nil
	}
	return []share.DiscoveredTool{{Name: info.Name, Source: share.SourceKindNative, Description: info.Description, Schema: share.CloneSchema(schema), ForegroundOnly: info.ForegroundOnly}}, nil
}
