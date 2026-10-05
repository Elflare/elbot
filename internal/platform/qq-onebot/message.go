package qqonebot

import (
	"encoding/json"
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
	segments, ok := decodeMessageSegments(raw)
	if !ok {
		return NormalizedMessage{}, false
	}
	return convertSegments(segments, selfID, ordinaryInput), true
}

func decodeMessageSegments(raw json.RawMessage) ([]Segment, bool) {
	var segments []Segment
	if json.Unmarshal(raw, &segments) == nil {
		return segments, true
	}
	text := messageString(raw)
	if strings.HasPrefix(strings.TrimSpace(text), "[") && json.Unmarshal([]byte(text), &segments) == nil {
		return segments, true
	}
	return nil, false
}

func messageString(raw json.RawMessage) string {
	var text string
	if len(raw) > 0 && json.Unmarshal(raw, &text) == nil {
		return text
	}
	return ""
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
