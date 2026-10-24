// 整理后台预加载所需的工具声明和工具名单。
package preload

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	toolregistry "elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

// BackgroundSelections 只展开名称和标签，供上游授权；不做运行资格筛选。
func (s *PreloadService) BackgroundSelections(ctx context.Context, selectors []string) ([]share.BackgroundSelection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var selections []share.BackgroundSelection
	for _, name := range uniqueNames(selectors) {
		if name == "discover_tool" {
			continue
		}
		if _, exists := s.registry.Get(name); exists {
			selections = append(selections, share.BackgroundSelection{Names: []string{name}})
			continue
		}
		tag := normalizeToolTag(name)
		configured, err := s.tags.configuredToolNamesByTag(ctx, tag)
		if err != nil {
			return nil, err
		}
		roots := uniqueNames(append(s.registry.NamesByTag(tag), configured...))
		if len(roots) == 0 {
			roots, tag = []string{name}, ""
		}
		selections = append(selections, share.BackgroundSelection{Names: roots, Tag: tag})
	}
	return selections, nil
}

func (s *PreloadService) PrepareBackground(ctx context.Context, req share.BackgroundPreloadRequest) (share.BackgroundPreload, error) {
	record := globalevents.LogRecord{Category: globalevents.LogAudit, Module: "tool", Identity: globalevents.LogIdentity{SessionID: &req.SessionID}}
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
	skip := func(name, reason string) {
		record.EventName = "background_preload_skipped"
		record.Level = slog.LevelInfo
		record.ResultStatus = globalevents.ResultSkipped
		record.Content = fmt.Sprintf("background preload skipped: name=%s reason=%s", name, reason)
		_ = globalevents.EmitLog(ctx, record)
	}
	selections, err := s.BackgroundSelections(ctx, req.Names)
	if err != nil {
		return share.BackgroundPreload{}, err
	}
	var names []string
	tags := map[string][]string{}
	for _, selection := range selections {
		for _, name := range selection.Names {
			if req.AllowedToolNames != nil && !slices.Contains(req.AllowedToolNames, name) {
				skip(name, "not_authorized")
				continue
			}
			names = append(names, name)
			if selection.Tag != "" {
				tags[name] = append(tags[name], selection.Tag)
			}
		}
	}
	names = uniqueNames(names)
	preparedTools, err := s.PrepareTools(ctx, names)
	if err != nil {
		return share.BackgroundPreload{}, err
	}
	preparedSkills, err := s.PrepareSkills(ctx, req.SessionID, names)
	if err != nil {
		return share.BackgroundPreload{}, err
	}
	result := share.BackgroundPreload{
		Skills:          preparedSkills.Names,
		NeedsElyphRules: preparedTools.NeedsElyphRules || preparedSkills.NeedsElyphRules,
	}
	result.Update.Tools = append(preparedTools.Update.Tools, preparedSkills.Update.Tools...)
	for _, declaration := range req.ELwispTools {
		result.Update.Tools = append(result.Update.Tools, share.DiscoveredTool{
			Name: declaration.Name, CanonicalName: declaration.CanonicalName,
			Source: share.SourceKindELwisp, Description: declaration.Description,
			Schema:   share.CloneSchema(llm.ToolSchema{Name: declaration.CanonicalName, Description: declaration.Description, Parameters: declaration.Schema}),
			EventKey: declaration.EventKey, Endpoint: declaration.Endpoint,
			TimeoutSeconds: declaration.TimeoutSeconds, ForegroundOnly: declaration.ForegroundOnly,
		})
	}
	result.Update.Tools = toolregistry.BackgroundCachedTools(ctx, result.Update.Tools)
	for i, name := range names {
		if preparedTools.Accepted[i] || preparedSkills.Accepted[i] {
			result.Update.Tags = append(result.Update.Tags, tags[name]...)
		} else {
			skip(name, "not_found_or_not_allowed")
		}
	}
	result.Update.Tags = uniqueNames(result.Update.Tags)
	var sections []string
	for _, content := range []string{preparedTools.Content, preparedSkills.Content} {
		if strings.TrimSpace(content) != "" {
			sections = append(sections, content)
		}
	}
	result.SkillPrompt = strings.Join(sections, "\n\n---\n\n")
	return result, nil
}
