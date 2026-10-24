// 读取、解码并提交工具拥有的会话状态。
package state

import toolshare "elbot/internal/tool/share"

import (
 toolregistry "elbot/internal/tool/registry"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"elbot/internal/events"
	"elbot/internal/storage"
	
)

// StateService has no second writable cache. All updates merge the latest row.
type StateService struct {
 store storage.Store
 registry *toolregistry.Registry // Catalog used to resolve persisted discovery names.
}

func NewStateService(store storage.Store, registry *toolregistry.Registry) *StateService { return &StateService{store: store, registry: registry} }

func DecodeState(raw string) (toolshare.State, error) {
	fields, err := storage.DecodeSessionMetadata(raw)
	if err != nil {
		return toolshare.State{}, err
	}
	var state toolshare.State
	for key, target := range map[string]any{
		"discovered_tools": &state.DiscoveredTools, "tool_cache": &state.ToolCache,
		"tool_tags": &state.ToolTags, "elyph_rules_shown": &state.ElyphRulesShown,
	} {
		if raw, ok := fields[key]; ok {
			if err := json.Unmarshal(raw, target); err != nil {
				return toolshare.State{}, fmt.Errorf("decode tool metadata %s: %w", key, err)
			}
		}
	}
	state.DiscoveredTools = uniqueNames(state.DiscoveredTools)
	state.ToolCache = NormalizeCachedTools(state.ToolCache)
	state.ToolTags = uniqueNames(state.ToolTags)
	return state, nil
}

// Load reads the persisted row without decoding tool metadata.
func (s *StateService) Load(ctx context.Context, id string) (*storage.Session, error) {
	return s.store.Sessions().Get(ctx, id)
}

func (s *StateService) Snapshot(ctx context.Context, id string) (toolshare.State, error) {
	row, err := s.Load(ctx, id)
	if err != nil {
		return toolshare.State{}, err
	}
	return DecodeState(row.Metadata)
}

func (s *StateService) Commit(ctx context.Context, id string, update toolshare.StateUpdate) (toolshare.StateCommit, events.LogResult, error) {
	var result toolshare.StateCommit
	var callbackErr error
	row, err := s.store.Sessions().Mutate(ctx, id, func(row *storage.Session) (err error) {
		defer func() { callbackErr = err }()
		state, err := DecodeState(row.Metadata)
		if err != nil {
			return err
		}
		before := state.Names()
		var injected, existing []string
		for _, item := range NormalizeCachedTools(update.Tools) {
			if item.Name == "" {
				continue
			}
			if before[item.Name] {
				existing = append(existing, item.Name)
			} else {
				injected = append(injected, item.Name)
			}
			state.DiscoveredTools = append(state.DiscoveredTools, item.Name)
		}
		state.DiscoveredTools = uniqueNames(state.DiscoveredTools)
		state.ToolCache = MergeCachedTools(state.ToolCache, update.Tools)
		state.ToolTags = uniqueNames(append(state.ToolTags, update.Tags...))
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		for key, value := range map[string]any{
			"discovered_tools": state.DiscoveredTools, "tool_cache": state.ToolCache,
			"tool_tags": state.ToolTags,
		} {
			empty := false
			switch v := value.(type) {
			case []string:
				empty = len(v) == 0
			case []toolshare.DiscoveredTool:
				empty = len(v) == 0
			}
			if empty {
				delete(fields, key)
			} else if err := fields.Set(key, value); err != nil {
				return err
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
		// Decode the encoded form, so neither the input nor output aliases schemas.
		result.State, err = DecodeState(row.Metadata)
		result.Injected, result.Existing = uniqueNames(injected), uniqueNames(existing)
		return err
	})
	if err != nil {
		if callbackErr != nil || ctx.Err() != nil {
			return toolshare.StateCommit{}, events.LogResult{ResultStatus: events.ResultSkipped}, err
		}
		return toolshare.StateCommit{}, events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("save tool state: %v", err)}, err
	}
	result.Metadata = row.Metadata
	return result, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "tool discovery state saved"}, nil
}

func CachedTools(s toolshare.State, registry interface {
	Get(string) (toolshare.Tool, bool)
}, background bool) []toolshare.DiscoveredTool {
	cached := make([]toolshare.DiscoveredTool, 0, len(s.ToolCache)+len(s.DiscoveredTools))
	for _, item := range s.ToolCache {
		if background && item.Name == "discover_tool" {
			continue
		}
		cached = append(cached, item)
	}
	for _, name := range s.DiscoveredTools {
		if registry == nil || (background && name == "discover_tool") {
			continue
		}
		if t, ok := registry.Get(name); ok {
			cached = append(cached, toolshare.DiscoveredTool{Name: name, Source: toolshare.SourceKindNative, Description: t.Info().Description, Schema: t.Schema(), ForegroundOnly: t.Info().ForegroundOnly})
		}
	}
	cached = NormalizeCachedTools(cached)
	for i := range cached {
		cached[i].Schema = cloneSchema(cached[i].Schema)
	}
	return cached
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

func (s *StateService) DiscoveredToolsForSession(ctx context.Context, id string, background bool) ([]toolshare.DiscoveredTool, events.LogResult, error) {
	row, err := s.Load(ctx, id)
	if err != nil {
		result := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("tool_state_error=%v", err)}
		if ctx.Err() != nil {
			result.ResultStatus = events.ResultCanceled
		}
		return nil, result, err
	}
	state, err := DecodeState(row.Metadata)
	if err != nil {
		return nil, events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	items := CachedTools(state, s.registry, background)
	if background {
		items = toolregistry.BackgroundCachedTools(ctx, items)
	}
	return items, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "tool state loaded"}, nil
}
