package qqonebot

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"elbot/internal/platform"
)

type Event struct {
	Time        int64           `json:"time"`
	SelfID      int64           `json:"self_id"`
	PostType    string          `json:"post_type"`
	MessageType string          `json:"message_type"`
	SubType     string          `json:"sub_type"`
	MessageID   int64           `json:"message_id"`
	UserID      int64           `json:"user_id"`
	GroupID     int64           `json:"group_id"`
	Message     json.RawMessage `json:"message"`
	RawMessage  string          `json:"raw_message"`
	Sender      Sender          `json:"sender"`
}

type Sender struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
	Role     string `json:"role"`
}

type Segment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

type NormalizedMessage struct {
	Text     string
	ReplyID  string
	Mentions []platform.Mention
	Segments []platform.MessageSegment
}

func normalizeMessage(raw json.RawMessage, rawMessage string, selfID int64) NormalizedMessage {
	if msg, ok := normalizeMessageSegments(raw, selfID); ok {
		return msg
	}
	if len(raw) > 0 {
		return normalizePlainText(messageString(raw))
	}
	return normalizePlainText(rawMessage)
}

func normalizeMessageSegments(raw json.RawMessage, selfID int64) (NormalizedMessage, bool) {
	var segments []Segment
	if json.Unmarshal(raw, &segments) == nil {
		return normalizeSegments(segments, selfID), true
	}
	text := messageString(raw)
	if strings.HasPrefix(strings.TrimSpace(text), "[") && json.Unmarshal([]byte(text), &segments) == nil {
		return normalizeSegments(segments, selfID), true
	}
	return NormalizedMessage{}, false
}

func messageString(raw json.RawMessage) string {
	var text string
	if len(raw) > 0 && json.Unmarshal(raw, &text) == nil {
		return text
	}
	return ""
}

func normalizeSegments(segments []Segment, selfID int64) NormalizedMessage {
	var out NormalizedMessage
	parts := []string{}
	self := fmt.Sprint(selfID)
	for _, seg := range segments {
		switch seg.Type {
		case "text":
			text := segmentDataString(seg.Data, "text")
			parts = append(parts, text)
			if text != "" {
				out.Segments = append(out.Segments, platform.MessageSegment{Type: platform.SegmentText, Text: text})
			}

		case "at":
			qq := strings.TrimSpace(segmentDataString(seg.Data, "qq"))
			if qq == "" || qq == "all" {
				continue
			}
			out.Mentions = append(out.Mentions, platform.Mention{UserID: qq})
			if qq != self {
				text := atText(qq, "")
				parts = append(parts, text)
				out.Segments = append(out.Segments, platform.MessageSegment{Type: platform.SegmentAt, Text: text, UserID: qq})
			}
		case "reply":
			out.ReplyID = strings.TrimSpace(segmentDataString(seg.Data, "id"))
		case "image":
			parts = append(parts, "[图片]")
			out.Segments = append(out.Segments, imageSegment(seg.Data))
		case "record":
			parts = append(parts, "[语音]")
			out.Segments = append(out.Segments, fileSegment("语音", seg.Data))
		case "video":
			parts = append(parts, "[视频]")
			out.Segments = append(out.Segments, fileSegment("视频", seg.Data))
		case "file":
			parts = append(parts, "[文件]")
			out.Segments = append(out.Segments, fileSegment("文件", seg.Data))
		case "face":

			parts = append(parts, "[表情]")
		default:
			if seg.Type != "" {
				parts = append(parts, "["+seg.Type+"]")
			}
		}
	}
	out.Text = cleanText(strings.Join(parts, ""))
	if len(out.Segments) == 0 && out.Text != "" {
		out.Segments = append(out.Segments, platform.MessageSegment{Type: platform.SegmentText, Text: out.Text})
	}
	return out
}

func normalizePlainText(text string) NormalizedMessage {
	text = cleanText(text)
	if text == "" {
		return NormalizedMessage{}
	}
	return NormalizedMessage{Text: text, Segments: []platform.MessageSegment{{Type: platform.SegmentText, Text: text}}}
}

func imageSegment(data map[string]any) platform.MessageSegment {
	file := firstNonEmpty(segmentDataString(data, "file"), segmentDataString(data, "filename"))
	url := strings.TrimSpace(segmentDataString(data, "url"))
	if url == "" && isDirectImageURL(file) {
		url = file
	}
	return platform.MessageSegment{Type: platform.SegmentImage, URL: url, Name: file}
}

func isDirectImageURL(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "base64://") || strings.HasPrefix(value, "data:") || strings.HasPrefix(value, "file://")
}

func fileSegment(kind string, data map[string]any) platform.MessageSegment {
	return platform.MessageSegment{Type: platform.SegmentFile, Text: kind, URL: strings.TrimSpace(segmentDataString(data, "url")), Name: firstNonEmpty(segmentDataString(data, "name"), segmentDataString(data, "file"), segmentDataString(data, "filename"), segmentDataString(data, "file_id")), Size: segmentDataInt64(data, "file_size")}
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
		return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%f", v), "0"), ".")
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func cleanText(text string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

func atText(qq, name string) string {
	qq = strings.TrimSpace(qq)
	name = strings.TrimSpace(name)
	if name == "" {
		return "[at qq:" + qq + "]"
	}
	return "[at " + name + " qq:" + qq + "]"
}

func senderName(sender Sender) string {
	if name := strings.TrimSpace(sender.Card); name != "" {
		return name
	}
	return strings.TrimSpace(sender.Nickname)
}

func displayName(sender Sender, userID int64) string {
	return senderName(sender)
}
