package toolrun

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"elbot/internal/storage"
	"elbot/internal/tool"
)

// State is a detached view of the tool-owned session metadata.
type State struct {
	DiscoveredTools      []string
	ToolCache            []CachedTool
	ToolTags             []string
	ShownRuleCardFormats []string
}

type StateUpdate struct {
	Tools                []CachedTool
	Tags                 []string
	ShownRuleCardFormats []string
}

type StateCommit struct {
	State    State
	Metadata string
	Injected []string
	Existing []string
}

// StateService has no second writable cache. All updates merge the latest row.
type StateService struct{ store storage.Store }

func NewStateService(store storage.Store) *StateService { return &StateService{store: store} }

func DecodeState(raw string) (State, error) {
	fields, err := storage.DecodeSessionMetadata(raw)
	if err != nil {
		return State{}, err
	}
	var state State
	for key, target := range map[string]any{
		"discovered_tools": &state.DiscoveredTools, "tool_cache": &state.ToolCache,
		"tool_tags": &state.ToolTags, "shown_rule_card_formats": &state.ShownRuleCardFormats,
	} {
		if raw, ok := fields[key]; ok {
			if err := json.Unmarshal(raw, target); err != nil {
				return State{}, fmt.Errorf("decode tool metadata %s: %w", key, err)
			}
		}
	}
	state.DiscoveredTools = uniqueNames(state.DiscoveredTools)
	state.ToolCache = NormalizeCachedTools(state.ToolCache)
	state.ToolTags = uniqueNames(state.ToolTags)
	state.ShownRuleCardFormats = uniqueNames(state.ShownRuleCardFormats)
	return state, nil
}

func (s *StateService) Snapshot(ctx context.Context, id string) (State, error) {
	row, err := s.store.Sessions().Get(ctx, id)
	if err != nil {
		return State{}, err
	}
	return DecodeState(row.Metadata)
}

func (s *StateService) Commit(ctx context.Context, id string, update StateUpdate) (StateCommit, error) {
	var result StateCommit
	row, err := s.store.Sessions().Mutate(ctx, id, func(row *storage.Session) error {
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
		state.ShownRuleCardFormats = uniqueNames(append(state.ShownRuleCardFormats, update.ShownRuleCardFormats...))
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		for key, value := range map[string]any{
			"discovered_tools": state.DiscoveredTools, "tool_cache": state.ToolCache,
			"tool_tags": state.ToolTags, "shown_rule_card_formats": state.ShownRuleCardFormats,
		} {
			empty := false
			switch v := value.(type) {
			case []string:
				empty = len(v) == 0
			case []CachedTool:
				empty = len(v) == 0
			}
			if empty {
				delete(fields, key)
			} else if err := fields.Set(key, value); err != nil {
				return err
			}
		}
		row.Metadata, err = fields.Encode()
		if err != nil {
			return err
		}
		row.UpdatedAt = storage.Now()
		// Decode the encoded form, so neither the input nor output aliases schemas.
		result.State, err = DecodeState(row.Metadata)
		result.Injected, result.Existing = uniqueNames(injected), uniqueNames(existing)
		return err
	})
	if err != nil {
		return StateCommit{}, fmt.Errorf("save tool state: %w", err)
	}
	result.Metadata = row.Metadata
	return result, nil
}

func (s State) Names() map[string]bool {
	out := map[string]bool{}
	for _, name := range s.DiscoveredTools {
		out[name] = true
	}
	for _, item := range s.ToolCache {
		if item.Name != "" {
			out[item.Name] = true
		}
	}
	return out
}

func (s State) CachedTools(registry *tool.Registry, background bool) []CachedTool {
	cached := make([]CachedTool, 0, len(s.ToolCache)+len(s.DiscoveredTools))
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
			cached = append(cached, CachedTool{Name: name, Source: SourceKindNative, Description: t.Info().Description, Schema: t.Schema(), ForegroundOnly: t.Info().ForegroundOnly})
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
