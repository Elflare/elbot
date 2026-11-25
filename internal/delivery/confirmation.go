package delivery

import (
	"bytes"
	controlshare "elbot/internal/control/share"
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
	ContentSessionList ContentBlockKind = "session_list"
	ContentPreview     ContentBlockKind = "preview"
	ContentQuestion    ContentBlockKind = "question"
	ContentText        ContentBlockKind = "text"
	ContentDiff        ContentBlockKind = "diff"
	ContentFields      ContentBlockKind = "fields"
	ContentCode        ContentBlockKind = "code"
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
	decoder := json.NewDecoder(bytes.NewReader(raw.Content))
	decoder.UseNumber()
	var content any
	var err error
	switch raw.Kind {
	case ContentSessionList:
		content, err = decodeBlockContent[controlshare.ListSessionsResult](decoder)
	case ContentPreview:
		content, err = decodeBlockContent[[]ContentBlock](decoder)
	case ContentQuestion:
		content, err = decodeBlockContent[QuestionContent](decoder)
	case ContentFields:
		content, err = decodeBlockContent[[]ContentField](decoder)
	case ContentCode:
		content, err = decodeBlockContent[CodeContent](decoder)
	case ContentText, ContentDiff:
		content, err = decodeBlockContent[string](decoder)
	default:
		return fmt.Errorf("unsupported content block kind %q", raw.Kind)
	}
	if err != nil {
		return err
	}
	*b = ContentBlock{Kind: raw.Kind, Content: content}
	return nil
}

func decodeBlockContent[T any](decoder *json.Decoder) (T, error) {
	var value T
	err := decoder.Decode(&value)
	return value, err
}

// ContentBlocksText renders structured blocks for consumers that only accept text.
func ContentBlocksText(blocks []ContentBlock) string {
	var parts []string
	for _, block := range blocks {
		var content string
		switch block.Kind {
		case ContentSessionList:
			content = SessionListText(block.Content.(controlshare.ListSessionsResult))
		case ContentPreview:
			content = previewOperation(operationPreviewBody(block.Content.([]ContentBlock)))
		case ContentQuestion:
			content = block.Content.(QuestionContent).text()
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

// Prefer changed lines over file metadata in a text-only operation preview.
func operationPreviewBody(blocks []ContentBlock) string {
	var path, diff string
	for _, block := range blocks {
		switch block.Kind {
		case ContentFields:
			for _, field := range block.Content.([]ContentField) {
				if field.Key == "path" {
					path, _ = field.Value.(string)
				}
			}
		case ContentDiff:
			if diff == "" {
				diff = block.Content.(string)
			}
		}
	}
	if diff == "" {
		return ContentBlocksText(blocks)
	}
	var lines []string
	if path != "" {
		lines = append(lines, path)
	}
	inHunk := false
	for line := range strings.SplitSeq(diff, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			inHunk = true
		}
		if inHunk && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) {
			lines = append(lines, line)
			if len(lines) == 5 {
				break
			} // One extra line signals truncation.
		}
	}
	return strings.Join(lines, "\n")
}
func previewOperation(s string) string {
	lines := strings.SplitN(s, "\n", 5)
	clipped := len(lines) > 4
	if clipped {
		s = strings.Join(lines[:4], "\n")
	}
	r := []rune(s)
	if len(r) > 240 || clipped && len(r) == 240 {
		r = r[:239]
		clipped = true
	}
	if clipped {
		return string(r) + "…"
	}
	return string(r)
}
func ActionHints(actions []Action) string {
	parts := make([]string, 0, len(actions))
	for _, a := range actions {
		parts = append(parts, a.Command+" "+a.Label)
	}
	return strings.Join(parts, " · ")
}
func WithActionHints(body string, actions []Action) string {
	if hints := ActionHints(actions); hints != "" {
		return strings.TrimSpace(body + "\n\n" + hints)
	}
	return body
}
