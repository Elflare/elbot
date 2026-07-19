package llm

import (
	"regexp"
	"strings"
)

func CloneMessages(messages []LLMMessage) []LLMMessage {
	if messages == nil {
		return nil
	}
	out := append([]LLMMessage(nil), messages...)
	for i := range out {
		out[i].Segments = append([]MessageSegment(nil), messages[i].Segments...)
		out[i].ToolCalls = append([]ToolCallRequest(nil), messages[i].ToolCalls...)
	}
	return out
}

func TextSegments(text string) []MessageSegment {
	if text == "" {
		return nil
	}
	return []MessageSegment{{Type: SegmentText, Text: text}}
}

// SetSegmentText replaces all text content while retaining non-text segments.
func SetSegmentText(segments []MessageSegment, text string) []MessageSegment {
	out := make([]MessageSegment, 0, len(segments)+1)
	inserted := false
	for _, segment := range segments {
		if segment.Type != SegmentText {
			out = append(out, segment)
			continue
		}
		if !inserted && text != "" {
			segment.Text = text
			out = append(out, segment)
			inserted = true
		}
	}
	if !inserted && text != "" {
		out = append([]MessageSegment{{Type: SegmentText, Text: text}}, out...)
	}
	return out
}

// SegmentsTextOnly returns only text segment content.
func SegmentsTextOnly(segments []MessageSegment) string {
	var text strings.Builder
	for _, segment := range segments {
		if segment.Type != SegmentText {
			continue
		}
		text.WriteString(segment.Text)
	}
	return strings.TrimSpace(text.String())
}

// SegmentsContentText returns readable plain text for storage, logs and summary.
func SegmentsContentText(segments []MessageSegment) string {
	var text strings.Builder
	for _, segment := range segments {
		switch segment.Type {
		case SegmentText:
			text.WriteString(segment.Text)
		case SegmentImage:
			writeSegmentLabel(&text, "图片", displaySegmentURL(segment.URL), segment.Name, segment.Text, segment.MIMEType)
		case SegmentFile:
			writeSegmentLabel(&text, "文件", displaySegmentURL(segment.URL), segment.Name, segment.Text, segment.MIMEType)
		}
	}
	return strings.TrimSpace(text.String())
}

func displaySegmentURL(value string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "data:") {
		return ""
	}
	return value
}

func PrependSegmentText(segments []MessageSegment, prefix string) []MessageSegment {
	if prefix == "" {
		return segments
	}
	out := append([]MessageSegment(nil), segments...)
	for i := range out {
		if out[i].Type == SegmentText {
			out[i].Text = prefix + out[i].Text
			return out
		}
	}
	return append([]MessageSegment{{Type: SegmentText, Text: prefix}}, out...)
}

func AppendSegmentText(segments []MessageSegment, suffix string) []MessageSegment {
	if suffix == "" {
		return segments
	}
	out := append([]MessageSegment(nil), segments...)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Type == SegmentText {
			out[i].Text += suffix
			return out
		}
	}
	return append(out, MessageSegment{Type: SegmentText, Text: suffix})
}

func ReplaceSegmentText(segments []MessageSegment, pattern *regexp.Regexp, replacement string, all bool) []MessageSegment {
	if pattern == nil {
		return segments
	}
	out := append([]MessageSegment(nil), segments...)
	for i := range out {
		if out[i].Type != SegmentText {
			continue
		}
		if all {
			out[i].Text = pattern.ReplaceAllString(out[i].Text, replacement)
			continue
		}
		loc := pattern.FindStringIndex(out[i].Text)
		if loc == nil {
			continue
		}
		out[i].Text = out[i].Text[:loc[0]] + pattern.ReplaceAllString(out[i].Text[loc[0]:loc[1]], replacement) + out[i].Text[loc[1]:]
		return out
	}
	return out
}

func ImageSegments(segments []MessageSegment) []MessageSegment {
	return segmentsByType(segments, SegmentImage)
}

func FileSegments(segments []MessageSegment) []MessageSegment {
	return segmentsByType(segments, SegmentFile)
}

func HasImageSegment(segments []MessageSegment) bool {
	for _, segment := range segments {
		if segment.Type == SegmentImage {
			return true
		}
	}
	return false
}

func MessagesHaveImageSegment(messages []LLMMessage) bool {
	for _, message := range messages {
		if HasImageSegment(message.Segments) {
			return true
		}
	}
	return false
}

func LatestUserSegments(messages []LLMMessage) []MessageSegment {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleUser {
			return messages[i].Segments
		}
	}
	return nil
}

func LatestUserSegmentTextOnly(messages []LLMMessage) string {
	return SegmentsTextOnly(LatestUserSegments(messages))
}

func LatestUserSegmentContentText(messages []LLMMessage) string {
	return SegmentsContentText(LatestUserSegments(messages))
}

func SetLatestUserSegments(messages []LLMMessage, segments []MessageSegment) []LLMMessage {
	out := append([]LLMMessage(nil), messages...)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == RoleUser {
			out[i].Segments = append([]MessageSegment(nil), segments...)
			return out
		}
	}
	return out
}

func PrependLatestUserSegmentText(messages []LLMMessage, prefix string) []LLMMessage {
	return editLatestUserSegments(messages, func(segments []MessageSegment) []MessageSegment {
		return PrependSegmentText(segments, prefix)
	})
}

func AppendLatestUserSegmentText(messages []LLMMessage, suffix string) []LLMMessage {
	return editLatestUserSegments(messages, func(segments []MessageSegment) []MessageSegment {
		return AppendSegmentText(segments, suffix)
	})
}

func ReplaceLatestUserSegmentText(messages []LLMMessage, pattern *regexp.Regexp, replacement string, all bool) []LLMMessage {
	return editLatestUserSegments(messages, func(segments []MessageSegment) []MessageSegment {
		return ReplaceSegmentText(segments, pattern, replacement, all)
	})
}

func AppendSystemSegmentText(messages []LLMMessage, text string) []LLMMessage {
	text = strings.TrimSpace(text)
	if text == "" {
		return messages
	}
	out := append([]LLMMessage(nil), messages...)
	for i := range out {
		if out[i].Role != RoleSystem {
			continue
		}
		if strings.TrimSpace(SegmentsTextOnly(out[i].Segments)) == "" {
			out[i].Segments = TextSegments(text)
		} else {
			out[i].Segments = AppendSegmentText(out[i].Segments, "\n\n"+text)
		}
		return out
	}
	return append([]LLMMessage{{Role: RoleSystem, Segments: TextSegments(text)}}, out...)
}

func editLatestUserSegments(messages []LLMMessage, edit func([]MessageSegment) []MessageSegment) []LLMMessage {
	out := append([]LLMMessage(nil), messages...)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == RoleUser {
			out[i].Segments = edit(out[i].Segments)
			return out
		}
	}
	return out
}

func segmentsByType(segments []MessageSegment, typ MessageSegmentType) []MessageSegment {
	out := make([]MessageSegment, 0)
	for _, segment := range segments {
		if segment.Type == typ {
			out = append(out, segment)
		}
	}
	return out
}

func writeSegmentLabel(text *strings.Builder, kind string, values ...string) {
	if text.Len() > 0 {
		text.WriteString(" ")
	}
	text.WriteString("[")
	text.WriteString(kind)
	if value := firstNonEmpty(values...); value != "" {
		text.WriteString(": ")
		text.WriteString(value)
	}
	text.WriteString("]")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
