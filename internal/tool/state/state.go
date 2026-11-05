// 读取、解码并提交工具拥有的会话状态。
package state

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"elbot/internal/storage"
	toolshare "elbot/internal/tool/share"
)

// StateService has no second writable cache. All updates merge the latest row.
type StateService struct {
	backgroundDiscoveredTools func([]toolshare.DiscoveredTool) []toolshare.DiscoveredTool // 根部注入的后台声明资格筛选。
	store                     interface {
		Get(context.Context, string) (*storage.Session, error)
		Mutate(context.Context, string, func(*storage.Session) error) (*storage.Session, error)
	} // 持久化会话状态；本服务不维护第二份可写缓存。
}

func NewStateService(store interface {
	Get(context.Context, string) (*storage.Session, error)
	Mutate(context.Context, string, func(*storage.Session) error) (*storage.Session, error)
}, backgroundDiscoveredTools func([]toolshare.DiscoveredTool) []toolshare.DiscoveredTool) *StateService {
	return &StateService{store: store, backgroundDiscoveredTools: backgroundDiscoveredTools}
}

func DecodeState(raw string) (toolshare.State, error) {
	fields, err := storage.DecodeSessionMetadata(raw)
	if err != nil {
		return toolshare.State{}, err
	}
	var state toolshare.State
	for key, target := range map[string]any{
		"discovered_tools": &state.DiscoveredTools,
		"tool_tags":        &state.ToolTags, "elyph_rules_shown": &state.ElyphRulesShown,
	} {
		if raw, ok := fields[key]; ok {
			if err := json.Unmarshal(raw, target); err != nil {
				return toolshare.State{}, fmt.Errorf("decode tool metadata %s: %w", key, err)
			}
		}
	}
	state.DiscoveredTools = normalizeDiscoveredTools(state.DiscoveredTools)
	state.ToolTags = uniqueNames(state.ToolTags)
	return state, nil
}

// PrepareUpdate 根据调用方的会话快照生成工具字段补丁，不读取或写入数据库。
// 返回值不含其他模块的元数据；调用方须与工具结果在同一事务中提交。
func PrepareUpdate(raw string, update toolshare.StateUpdate) (storage.SessionMetadata, error) {
	state, err := DecodeState(raw)
	if err != nil {
		return nil, err
	}
	return prepareUpdate(&state, update)
}

// prepareUpdate 合并声明与标签，并编码工具拥有的非空字段。
func prepareUpdate(state *toolshare.State, update toolshare.StateUpdate) (storage.SessionMetadata, error) {
	for _, group := range update.ReplaceGroups {
		retained := make([]toolshare.DiscoveredTool, 0, len(state.DiscoveredTools))
		for _, existing := range state.DiscoveredTools {
			if existing.Source == toolshare.SourceKindELwisp && existing.EventKey == group.EventKey {
				continue
			}
			retained = append(retained, existing)
		}
		state.DiscoveredTools = retained
	}
	if len(update.ReplaceGroups) > 0 {
		byName := make(map[string]toolshare.DiscoveredTool, len(state.DiscoveredTools)+len(update.Tools))
		for _, existing := range state.DiscoveredTools {
			byName[existing.Schema.Name] = existing
		}
		for _, incoming := range update.Tools {
			if existing, ok := byName[incoming.Schema.Name]; ok && (existing.Source != incoming.Source || existing.EventKey != incoming.EventKey) {
				return nil, fmt.Errorf("tool %q conflicts with another session tool group", incoming.Schema.Name)
			}
			byName[incoming.Schema.Name] = incoming
		}
	}
	state.DiscoveredTools = mergeDiscoveredTools(state.DiscoveredTools, update.Tools)
	state.ToolTags = uniqueNames(append(state.ToolTags, update.Tags...))
	fields := storage.SessionMetadata{}
	if len(state.DiscoveredTools) > 0 || len(update.ReplaceGroups) > 0 {
		if err := fields.Set("discovered_tools", state.DiscoveredTools); err != nil {
			return nil, err
		}
	}
	if len(state.ToolTags) > 0 {
		_ = fields.Set("tool_tags", state.ToolTags)
	}
	return fields, nil
}

func (s *StateService) Commit(ctx context.Context, id string, update toolshare.StateUpdate) (result toolshare.StateCommit, err error) {

	for _, group := range update.ReplaceGroups {
		if group.SessionID != id {
			return toolshare.StateCommit{}, fmt.Errorf("tool registration belongs to session %q, not %q", group.SessionID, id)
		}
	}
	row, err := s.store.Mutate(ctx, id, func(row *storage.Session) (err error) {
		state, err := DecodeState(row.Metadata)
		if err != nil {
			return err
		}
		before := discoveredNames(state.DiscoveredTools)
		var injected, existing []string
		for _, item := range update.Tools {
			name := item.CanonicalName
			if name == "" {
				name = item.Name
			}
			if before[name] {
				existing = append(existing, name)
			} else {
				injected = append(injected, name)
			}
		}
		patch, err := prepareUpdate(&state, update)
		if err != nil {
			return err
		}
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		for _, key := range []string{"discovered_tools", "tool_tags"} {
			if value, ok := patch[key]; ok {
				fields[key] = value
			} else {
				delete(fields, key)
			}
		}
		metadata, err := fields.Encode()
		if err != nil {
			return err
		}
		if metadata != row.Metadata {
			row.Metadata = metadata
			row.UpdatedAt = storage.Now()
		}
		result.Injected, result.Existing = uniqueNames(injected), uniqueNames(existing)
		return nil
	})
	if err != nil {
		return toolshare.StateCommit{}, err
	}
	result.Metadata = row.Metadata
	return result, nil
}

func uniqueNames(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func (s *StateService) DiscoveredToolsForSession(ctx context.Context, id string, background bool) (items []toolshare.DiscoveredTool, err error) {

	row, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	state, err := DecodeState(row.Metadata)
	if err != nil {
		return nil, err
	}
	items = state.DiscoveredTools
	if background {
		items = s.backgroundDiscoveredTools(items)
	}
	return items, nil
}
