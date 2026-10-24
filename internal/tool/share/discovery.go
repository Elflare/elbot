// 定义发现结果、详情格式和内容提供者契约。
package share

import (
	"context"
	"elbot/internal/llm"
)

type DiscoveryResult struct {
	Tools  []DiscoveryItem `json:"tools,omitempty"`
	Errors []DiscoveryError `json:"errors,omitempty"`
}

type DiscoveryError struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type PublicInfo struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	Source         string `json:"source"`
	ForegroundOnly bool   `json:"foreground_only,omitempty"`
}

type DiscoveryItem struct {
	// NeedsElyphRules is internal presentation metadata, never part of the schema.
	NeedsElyphRules bool            `json:"-"`
	Info            PublicInfo      `json:"info"`
	Schema          *llm.ToolSchema `json:"schema,omitempty"`
	Detail          string          `json:"detail,omitempty"`
	DetailBlock     DetailBlock     `json:"-"`
}

type DetailBlock struct {
	Content string
	Format  string
}

// DiscoveryText keeps discovery presentation separate from session rule state.
type DiscoveryText struct {
	Summary string
	Details string
}

type DetailProvider interface {
	Detail() string
	ActivateTools() []string
}

// DiscoveryContentProvider lets a tool customize the text shown when it is discovered
// via discover_tool. The schema injection (Data) is unaffected.
// override=true: custom content is rendered as a standalone details block.
// override=false: custom content is
// appended after that line as a standalone block.
type DiscoveryContentProvider interface {
	DiscoveryContent() (content string, override bool)
}

// ContextDiscoveryContentProvider provides discovery text that depends on the
// current tool execution context, such as the active workspace.
type ContextDiscoveryContentProvider interface {
	DiscoveryContent(context.Context) (content string, override bool, err error)
}

type StructuredDetailProvider interface {
	DetailBlock() DetailBlock
}

// LazyDetailProvider loads detail content only when a tool is discovered or preloaded.
// The context carries the actor used by the caller for access checks.
type LazyDetailProvider interface {
	LoadDetail(context.Context) (DetailBlock, error)
}

const (
	MetadataActivateTools = "activate_tools"
)
