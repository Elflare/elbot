// 定义工具状态、缓存条目、状态更新与提交结果。
package share

import (
	"elbot/internal/llm"
)

type SourceKind string

const (
	SourceKindNative SourceKind = "native"
	SourceKindELwisp SourceKind = "elwisp"
)

type DiscoveredTool struct {
	Name           string         `json:"name"`
	CanonicalName  string         `json:"canonical_name,omitempty"`
	Source         SourceKind     `json:"source"`
	Description    string         `json:"description,omitempty"`
	Schema         llm.ToolSchema `json:"schema"`
	ELwispName     string         `json:"elwisp_name,omitempty"`
	EventKey       string         `json:"event_key,omitempty"`
	Endpoint       string         `json:"endpoint,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	ForegroundOnly bool           `json:"foreground_only,omitempty"`
}

// State is a detached view of the tool-owned session metadata.
type State struct {
	DiscoveredTools []string
	ToolCache       []DiscoveredTool
	ToolTags        []string
	// ElyphRulesShown is committed with dialogue messages, never by StateService.
	ElyphRulesShown bool
}

type StateUpdate struct {
	Tools []DiscoveredTool
	Tags  []string
}

type StateCommit struct {
	State    State
	Metadata string
	Injected []string
	Existing []string
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
