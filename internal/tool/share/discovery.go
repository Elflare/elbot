// 定义发现结果、详情格式和内容提供者契约。
package share

import (
	"context"
	"elbot/internal/llm"
	"strings"
)

type DiscoveryResult struct {
	Tools  []DiscoveryItem  `json:"tools,omitempty"`
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

// DetailProvider 提供按调用上下文加载的技能详情和需要激活的工具名称。
type DetailProvider interface {
	LoadDetail(context.Context) (DetailBlock, error)
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

const (
	MetadataActivateTools = "activate_tools"
)

// RenderDetailBlocks 只拼接业务详情，不处理会话相关的语言规则。
func RenderDetailBlocks(blocks []DetailBlock) string {
	var parts []string
	for _, block := range blocks {
		if content := strings.TrimSpace(block.Content); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}
