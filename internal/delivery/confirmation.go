package delivery

import (
	"encoding/json"
	"strings"
)

// ContentBlock preserves the display category of message content until a
// consumer chooses its final representation.
type ContentBlockKind string

type ContentBlock struct {
	Kind    ContentBlockKind `json:"kind"`    // Selects the content renderer.
	Content any              `json:"content"` // Text or structured JSON, depending on Kind.
}

const (
	ContentText      ContentBlockKind = "text"
	ContentDiff      ContentBlockKind = "diff"
	ContentArguments ContentBlockKind = "arguments"
	ContentMarkdown  ContentBlockKind = "markdown"
)

// ContentBlocksText renders structured blocks for consumers that only accept text.
func ContentBlocksText(blocks []ContentBlock) string {
	var parts []string
	for _, block := range blocks {
		var content string
		switch block.Kind {
		case ContentArguments:
			encoded, err := json.MarshalIndent(block.Content, "", "  ")
			if err != nil {
				continue
			}
			content = "```json\n" + string(encoded) + "\n```"
		case ContentText, ContentDiff, ContentMarkdown:
			content, _ = block.Content.(string)
		}
		if block.Kind == ContentDiff {
			content = "```diff\n" + content + "\n```"
		}
		if content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n")
}

// Confirmation 是命令确认和工具风险确认共用的展示契约。
// 等待、授权和执行由各自业务负责，Action.ID 不是授权凭据。
type Confirmation struct {
	Actions []Action `json:"actions"` // 由业务生成的可选命令操作，展示方直接提交 Command。
}
