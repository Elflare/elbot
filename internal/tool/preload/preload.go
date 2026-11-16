// 预加载工具和 Skill，生成展示材料与待提交状态。
package preload

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/tool/share"
)

type PreloadOptions struct {
	RegisterTools func(context.Context, share.Source, share.ToolRegistrationScope, []share.ToolDefinition) (share.StateUpdate, error) // 根部注入的统一注册入口。
	Resolver      interface {
		Resolve(context.Context, []share.ToolDefinition) (*share.DiscoveryResult, map[string]share.ToolDefinition, error)
	} // 根部注入的发现解析。
	NativeDiscoveredToolsFromDiscovery func(*share.DiscoveryResult) []share.DiscoveredTool // 已解析结果转换。
	BackgroundDiscoveredTools          func([]share.DiscoveredTool) []share.DiscoveredTool // 后台资格筛选。
	Registry                           interface {
		Get(string) (share.ToolDefinition, bool)
		Query(context.Context, share.ToolQuery) ([]share.ToolDefinition, error)
	} // 已配置统一权限策略的工具目录。
	TagsPath string                // 标签配置文件路径；为空时使用 Tags。
	Tags     config.ToolTagsConfig // 不使用配置文件时的标签配置。
}

// PreloadService 准备工具和展示材料，不写会话状态，也不触发 Skill 扫描。
// 调用方提供身份、模型、工作目录和前后台事实；资格筛选统一交给 Registry。
type PreloadService struct {
	PreloadOptions
	tags *toolTagConfigSource // 标签配置读取。
}

func NewPreloadService(opts PreloadOptions) *PreloadService {
	return &PreloadService{PreloadOptions: opts, tags: newToolTagConfigSource(opts.TagsPath, opts.Tags)}
}

func (s *PreloadService) PrepareTools(ctx context.Context, names []string) (share.ToolPreload, error) {
	if err := ctx.Err(); err != nil {
		return share.ToolPreload{}, err
	}
	result := share.ToolPreload{Accepted: make([]bool, len(names))}
	seen := map[string]bool{}
	var content []string
	for i, name := range names {
		if definition, ok := s.Registry.Get(strings.TrimSpace(name)); ok && definition.LoadDetail != nil {
			continue
		}
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
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		blocks, tools, needsElyph, err := s.preloadDetails(ctx, facts.SessionID, details, targets, share.PolicyFromContext(ctx).DisableForegroundTools)
		if err != nil {
			return share.ToolPreload{}, err
		}
		if text := share.RenderDetailBlocks(blocks); text != "" {
			content = append(content, text)
		}
		result.Update.Tools = append(result.Update.Tools, tools...)
		result.NeedsElyphRules = result.NeedsElyphRules || needsElyph
		if tag != "" {
			result.Update.Tags = append(result.Update.Tags, tag)
		}
		result.Accepted[i] = true
	}
	result.Content = strings.Join(content, "\n\n")
	return result, nil
}

func (s *PreloadService) PrepareSkills(ctx context.Context, sessionID string, names []string) (share.SkillPreload, error) {
	candidates, err := s.Registry.Query(ctx, share.ToolQuery{Names: uniqueNames(names), Kind: "skill"})
	if err != nil {
		return share.SkillPreload{}, err
	}
	targets := make(map[string]share.ToolDefinition, len(candidates))
	for _, candidate := range candidates {
		targets[candidate.Definition.Name] = candidate
	}
	result := share.SkillPreload{Accepted: make([]bool, len(names))}
	var roots []share.ToolDefinition
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if root, ok := targets[name]; ok && !seen[name] {
			seen[name] = true
			roots = append(roots, root)
		}
	}
	details, targets, err := s.Resolver.Resolve(ctx, roots)
	if err != nil {
		return share.SkillPreload{}, err
	}
	blocks, tools, needsElyph, err := s.preloadDetails(ctx, sessionID, details, targets, share.PolicyFromContext(ctx).DisableForegroundTools)
	if err != nil {
		return share.SkillPreload{}, err
	}
	result.Update.Tools = append(result.Update.Tools, tools...)
	result.NeedsElyphRules = needsElyph
	for _, item := range details.Tools {
		if item.DetailBlock.Content != "" {
			result.Names = append(result.Names, item.Info.Name)
		}
	}
	for i, name := range names {
		name = strings.TrimSpace(name)
		if _, ok := targets[name]; !ok {
			result.Invalid = append(result.Invalid, name)
			continue
		}
		result.Accepted[i] = true
	}
	result.Content = share.RenderDetailBlocks(blocks)
	return result, nil
}

func (s *PreloadService) preloadDetails(ctx context.Context, sessionID string, details *share.DiscoveryResult, targets map[string]share.ToolDefinition, background bool) ([]share.DetailBlock, []share.DiscoveredTool, bool, error) {
	var blocks []share.DetailBlock
	needsElyph := false
	var tools []share.DiscoveredTool
	for _, item := range details.Tools {
		needsElyph = needsElyph || item.NeedsElyphRules
		if item.DetailBlock.Content != "" {
			blocks = append(blocks, item.DetailBlock)
		}
		for _, wrapper := range targets[item.Info.Name].ActivateTools {
			activated, err := s.preloadWrapper(ctx, sessionID, wrapper, background)
			if err != nil {
				return nil, nil, false, err
			}
			tools = append(tools, activated...)
		}
	}
	tools = append(tools, s.NativeDiscoveredToolsFromDiscovery(details)...)
	return blocks, tools, needsElyph, nil
}

func (s *PreloadService) discoverDirective(ctx context.Context, name string) (*share.DiscoveryResult, map[string]share.ToolDefinition, string, error) {
	name = strings.TrimSpace(name)
	query, tag := share.ToolQuery{Scope: share.QueryAvailable, Names: []string{name}, IncludeHidden: true, IncludeDiscover: true}, ""
	if _, exists := s.Registry.Get(name); !exists {
		tag = share.NormalizeToolTag(name)
		query.Scope, query.Names = share.QueryCatalog, nil
		candidates, err := s.Registry.Query(ctx, query)
		if err != nil {
			return nil, nil, "", err
		}
		names, err := s.NamesByTag(ctx, tag, candidates)
		if err != nil {
			return nil, nil, "", err
		}
		candidates = slices.DeleteFunc(candidates, func(candidate share.ToolDefinition) bool {
			return !slices.Contains(names, candidate.Definition.Name)
		})
		result, targets, err := s.Resolver.Resolve(ctx, candidates)
		return result, targets, tag, err
	}
	candidates, err := s.Registry.Query(ctx, query)
	if err != nil {
		return nil, nil, "", err
	}
	result, targets, err := s.Resolver.Resolve(ctx, candidates)
	return result, targets, tag, err
}

func (s *PreloadService) discoveryContent(ctx context.Context, details *share.DiscoveryResult, targets map[string]share.ToolDefinition, seen map[string]bool) (string, error) {
	var parts []string
	for _, item := range details.Tools {
		name := item.Info.Name
		if seen[name] {
			continue
		}
		candidate := targets[name]
		if candidate.DiscoveryContent == nil {
			continue
		}
		seen[name] = true
		content, _, err := candidate.DiscoveryContent(ctx)
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

func (s *PreloadService) preloadWrapper(ctx context.Context, sessionID, name string, background bool) ([]share.DiscoveredTool, error) {
	record := preloadLogRecord(ctx, sessionID)

	if name == "discover_tool" {
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
		globalevents.Log(ctx, record)
	}
	candidates, err := s.Registry.Query(ctx, share.ToolQuery{Names: []string{name}, IncludeHidden: true})
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		skip("not_found_or_not_allowed")
		return nil, nil
	}
	candidate := candidates[0]
	info := candidate.Definition
	// 可执行 Skill 的文档和自身 schema 分开提供，激活时直接读取所选对象的 schema。
	skill := candidate.LoadDetail != nil
	if background && !info.Hidden && !skill {
		result, _, err := s.Resolver.Resolve(ctx, candidates)
		if err != nil {
			return nil, err
		}
		return s.NativeDiscoveredToolsFromDiscovery(result), nil
	}
	if !candidate.Callable() {
		skip("document_only")
		return nil, nil
	}
	schema := candidate.Schema()
	return []share.DiscoveredTool{{Name: info.Name, Source: share.SourceKindNative, Description: info.Description, Schema: share.CloneSchema(schema), ForegroundOnly: info.ForegroundOnly}}, nil
}

func preloadLogRecord(ctx context.Context, sessionID string) globalevents.LogRecord {
	record := globalevents.LogRecord{Category: globalevents.LogCategoryAudit, Module: "tool"}
	record.Identity = globalevents.LogIdentityFromContext(ctx)
	record.Identity.SessionID = sessionID
	return record
}
