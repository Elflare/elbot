// 预加载工具和 Skill，生成展示材料与待提交状态。
package preload

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
	"elbot/internal/tool/share"
)

type PreloadOptions struct {
	RegisterTools func(context.Context, share.Source, share.ToolRegistrationScope, []share.ToolDefinition) (share.StateUpdate, error) // 根部注入的统一注册入口。
	Resolver      interface {
		Resolve(context.Context, []share.ToolDefinition) (*share.DiscoveryResult, map[string]share.ToolDefinition, error)
	} // 根部注入的发现解析。
	NativeDiscoveredToolsFromDiscovery func(*share.DiscoveryResult) []share.DiscoveredTool                     // 已解析结果转换。
	LoadDiscoveryContent               func(context.Context, share.ToolDefinition) (string, bool, bool, error) // 动态发现说明。
	BackgroundDiscoveredTools          func([]share.DiscoveredTool) []share.DiscoveredTool                     // 后台资格筛选。
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
		result.Update.Tools = append(result.Update.Tools, s.NativeDiscoveredToolsFromDiscovery(details)...)
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
	candidates, err := s.Registry.Query(ctx, share.ToolQuery{Names: uniqueNames(names), Kind: "skill"})
	if err != nil {
		return share.SkillPreload{}, err
	}
	targets := make(map[string]share.ToolDefinition, len(candidates))
	for _, candidate := range candidates {
		targets[candidate.Definition.Name] = candidate
	}
	record := preloadLogRecord(ctx, sessionID)

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
		if !seen[name] {
			block, err := candidate.LoadDetail(ctx)
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				record.EventName = "skill_preload_failed"
				record.Level = slog.LevelError
				record.ResultStatus = globalevents.ResultFailed
				if errors.Is(err, context.Canceled) {
					record.Level = slog.LevelInfo
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
			for _, wrapper := range candidate.ActivateTools {
				tools, err := s.preloadWrapper(ctx, sessionID, wrapper, share.PolicyFromContext(ctx).DisableForegroundTools)
				if err != nil {
					return share.SkillPreload{}, err
				}
				result.Update.Tools = append(result.Update.Tools, tools...)
			}
		}
		result.Accepted[i] = true
	}
	result.Content = share.RenderDetailBlocks(blocks)
	return result, nil
}

func (s *PreloadService) discoverDirective(ctx context.Context, name string) (*share.DiscoveryResult, map[string]share.ToolDefinition, string, error) {
	name = strings.TrimSpace(name)
	query, tag := share.ToolQuery{Names: []string{name}, Kind: "tool"}, ""
	if _, exists := s.Registry.Get(name); !exists {
		tag = share.NormalizeToolTag(name)
		query.Names = nil
	}
	candidates, err := s.Registry.Query(ctx, query)
	if err != nil {
		return nil, nil, "", err
	}
	if query.Names == nil {
		names, err := s.NamesByTag(ctx, tag, candidates)
		if err != nil {
			return nil, nil, "", err
		}
		candidates = slices.DeleteFunc(candidates, func(definition share.ToolDefinition) bool {
			return !slices.Contains(names, definition.Definition.Name)
		})
	}
	result, targets, err := s.discoverNames(ctx, candidates)
	return result, targets, tag, err
}

func (s *PreloadService) discoverNames(ctx context.Context, candidates []share.ToolDefinition) (*share.DiscoveryResult, map[string]share.ToolDefinition, error) {
	details, targets, err := s.Resolver.Resolve(ctx, candidates)
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
		content, _, _, err := s.LoadDiscoveryContent(ctx, candidate)
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
		_ = globalevents.EmitLog(ctx, record)
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
		result, _, err := s.discoverNames(ctx, candidates)
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
	record := globalevents.LogRecord{Category: globalevents.LogAudit, Module: "tool", Identity: globalevents.LogIdentity{SessionID: &sessionID}}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	record.Identity.RunID = &facts.RunID
	record.Identity.Attempt = &facts.Attempt
	record.Identity.RequestID = &facts.RequestID
	record.Identity.RootRequestID = &facts.RootRequestID
	return record
}
