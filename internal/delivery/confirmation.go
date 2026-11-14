package delivery

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	ContentText   ContentBlockKind = "text"
	ContentDiff   ContentBlockKind = "diff"
	ContentFields ContentBlockKind = "fields"
	ContentCode   ContentBlockKind = "code"
)

// ContentField is an independently renderable confirmation field.
type ContentField struct {
	Key   string `json:"key"`   // Stable machine-readable field name.
	Label string `json:"label"` // Human-readable field label.
	Value any    `json:"value"` // Structured value, preserving numbers, booleans and collections.
}

// CodeContent preserves source text and its syntax independently of formatting.
type CodeContent struct {
	Language string `json:"language"` // Syntax used by the code renderer.
	Content  string `json:"content"`  // Complete source or command text.
}

// UnmarshalJSON restores typed content for API and remote CLI consumers.
func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	var raw struct {
		Kind    ContentBlockKind `json:"kind"`
		Content json.RawMessage  `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var target any
	switch raw.Kind {
	case ContentFields:
		target = new([]ContentField)
	case ContentCode:
		target = new(CodeContent)
	case ContentText, ContentDiff:
		target = new(string)
	default:
		return fmt.Errorf("unsupported content block kind %q", raw.Kind)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw.Content))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	b.Kind = raw.Kind
	switch value := target.(type) {
	case *[]ContentField:
		b.Content = *value
	case *CodeContent:
		b.Content = *value
	case *string:
		b.Content = *value
	}
	return nil
}

// ContentBlocksText renders structured blocks for consumers that only accept text.
func ContentBlocksText(blocks []ContentBlock) string {
	var parts []string
	for _, block := range blocks {
		var content string
		switch block.Kind {
		case ContentFields:
			var lines []string
			for _, field := range block.Content.([]ContentField) {
				value, ok := field.Value.(string)
				if !ok {
					var encoded strings.Builder
					encoder := json.NewEncoder(&encoded)
					encoder.SetEscapeHTML(false)
					if err := encoder.Encode(field.Value); err != nil {
						panic(err)
					}
					value = strings.TrimSuffix(encoded.String(), "\n")
				}
				lines = append(lines, field.Label+"："+value)
			}
			content = strings.Join(lines, "\n")
		case ContentCode:
			code := block.Content.(CodeContent)
			content = "```" + code.Language + "\n" + code.Content + "\n```"
		case ContentText:
			content = block.Content.(string)
		case ContentDiff:
			content = "```diff\n" + block.Content.(string) + "\n```"
		default:
			panic(fmt.Errorf("unsupported content block kind %q", block.Kind))
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
