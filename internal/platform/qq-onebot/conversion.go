// conversion.go 负责普通输入与合并转发内容的消息片段转换，保留各场景的元数据规则。
// 这里只转换协议事实，不获取媒体或交付业务消息。

package qqonebot

import (
	"elbot/internal/platform/share"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type conversionScene uint8

const (
	ordinaryInput conversionScene = iota
	forwardContent
)

// convertSegments owns the OneBot-to-ElBot type mapping. The scene controls
// whether files and message-level metadata are retained; conversion never fetches media.
func convertSegments(segments []Segment, selfID int64, scene conversionScene) NormalizedMessage {
	var out NormalizedMessage
	var parts []string
	self := strconv.FormatInt(selfID, 10)
	for _, seg := range segments {
		label := segmentLabel(seg.Type)
		text := ""
		if label != "" {
			text = "[" + label + "]"
		}
		var part share.MessageSegment
		switch seg.Type {
		case "text":
			text = segmentDataString(seg.Data, "text")
		case "at":
			if scene == ordinaryInput {
				qq := strings.TrimSpace(segmentDataString(seg.Data, "qq"))
				if qq == "" || qq == "all" {
					continue
				}
				out.Mentions = append(out.Mentions, share.Mention{UserID: qq})
				if qq == self {
					continue
				}
				text = atText(qq, "")
				part = share.MessageSegment{Type: share.SegmentAt, Text: text, UserID: qq}
			}
		case "reply":
			if scene == ordinaryInput {
				out.ReplyID = strings.TrimSpace(segmentDataString(seg.Data, "id"))
				continue
			}
		case "image":
			text = ""
			part = imageSegment(seg.Data)
		case "record", "video", "file":
			if scene == ordinaryInput {
				part = fileSegment(label, seg.Data)
			}
		}
		parts = append(parts, text)
		if part.Type == "" && text != "" {
			part = textSegment(text)
		}
		if part.Type != "" {
			out.Segments = append(out.Segments, part)
		}
	}
	text := strings.Join(parts, "")
	if scene == ordinaryInput {
		text = cleanText(text)
	}
	out.Text = text
	return out
}

func segmentLabel(kind string) string {
	switch kind {
	case "record":
		return "语音"
	case "video":
		return "视频"
	case "file":
		return "文件"
	case "face":
		return "表情"
	default:
		return kind
	}
}

func normalizeForwardContent(raw json.RawMessage) []share.MessageSegment {
	if segments, ok := decodeMessageSegments(raw); ok {
		return convertSegments(segments, 0, forwardContent).Segments
	}
	if text := messageString(raw); text != "" {
		return []share.MessageSegment{textSegment(text)}
	}
	return nil
}

func normalizePlainText(text string) NormalizedMessage {
	text = cleanText(text)
	if text == "" {
		return NormalizedMessage{}
	}
	return NormalizedMessage{Text: text, Segments: []share.MessageSegment{textSegment(text)}}
}

func textSegment(text string) share.MessageSegment {
	return share.MessageSegment{Type: share.SegmentText, Text: text}
}

func imageSegment(data map[string]any) share.MessageSegment {
	file := firstNonEmpty(segmentDataString(data, "file"), segmentDataString(data, "filename"))
	url := strings.TrimSpace(segmentDataString(data, "url"))
	if url == "" && isDirectImageURL(file) {
		url = file
	}
	return share.MessageSegment{Type: share.SegmentImage, URL: url, Name: file, PlatformFileID: file, MIMEType: segmentDataString(data, "mime_type"), Size: segmentDataInt64(data, "file_size")}
}

func isDirectImageURL(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "base64://") || strings.HasPrefix(value, "data:") || strings.HasPrefix(value, "file://")
}

func fileSegment(kind string, data map[string]any) share.MessageSegment {
	return share.MessageSegment{Type: share.SegmentFile, Text: kind, PlatformFileID: firstNonEmpty(segmentDataString(data, "file_id"), segmentDataString(data, "file")), MIMEType: segmentDataString(data, "mime_type"), URL: strings.TrimSpace(segmentDataString(data, "url")), Name: firstNonEmpty(segmentDataString(data, "name"), segmentDataString(data, "file"), segmentDataString(data, "filename"), segmentDataString(data, "file_id")), Size: segmentDataInt64(data, "file_size")}
}

func segmentDataString(data map[string]any, key string) string {
	value, ok := data[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

func segmentDataInt64(data map[string]any, key string) int64 {
	value := segmentDataString(data, key)
	if value == "" {
		return 0
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
