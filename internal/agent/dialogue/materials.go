package dialogue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/storage"
)

// Material is an archived public content fragment. Index associates it with its
// owner's ordered records; the owner alone interprets those records.
type Material struct {
	Index int `json:"index"`
	// MessageID identifies the committed source, when the fragment has one.
	MessageID string               `json:"message_id,omitempty"`
	Segments  []llm.MessageSegment `json:"segments"`
}

// ArchiveMaterials returns a canonical copy. Existing references are never read
// or imported again, even when a temporary request URL accompanies them.
func (p *Preparer) ArchiveMaterials(ctx context.Context, messages []llm.LLMMessage) ([]llm.LLMMessage, error) {
	out := llm.CloneMessages(messages)
	for i := range out {
		for j, segment := range out[i].Segments {
			if segment.Type != llm.SegmentImage && segment.Type != llm.SegmentFile {
				continue
			}
			if segment.MediaID != "" {
				out[i].Segments[j].URL = ""
				continue
			}
			if p == nil || p.Media == nil {
				return nil, fmt.Errorf("media center is required to archive media")
			}
			imported, err := p.Media.Materialize(ctx, []llm.MessageSegment{segment})
			if err != nil {
				return nil, err
			}

			out[i].Segments[j] = imported[0]
		}
	}
	return out, ctx.Err()
}

// PrepareMaterials produces a disposable request view of committed content.
// Its release belongs to the request lifecycle, including all HTTP retries.
func (p *Preparer) PrepareMaterials(ctx context.Context, messages []llm.LLMMessage) ([]llm.LLMMessage, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, func() {}, err
	}
	if p == nil || p.Media == nil {
		for _, message := range messages {
			for _, segment := range message.Segments {
				if segment.MediaID != "" {
					return nil, func() {}, fmt.Errorf("media center is required to prepare media")
				}
			}
		}
		return llm.CloneMessages(messages), func() {}, nil
	}
	resolved, release, err := p.Media.AcquireForLLM(ctx, messages)
	if err != nil {
		return nil, release, err
	}
	missing := map[string]bool{}
	for i, message := range messages {
		for j, segment := range message.Segments {
			if segment.MediaID != "" && resolved[i].Segments[j].Type == llm.SegmentText {
				missing[segment.MediaID] = true
			}
		}
	}
	if len(missing) != 0 {
		ids := make([]string, 0, len(missing))
		for id := range missing {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		data, _ := json.Marshal(map[string]any{"media_ids": ids, "reason": "media unavailable; request contains placeholder text"})
		_ = events.EmitLog(ctx, events.LogRecord{Category: events.LogRuntime, Level: slog.LevelWarn, EventName: "dialogue_media_unavailable", Module: "dialogue", Type: events.ContentJSON, Content: string(data)})
		_, _ = events.NotificationRequested.Emit(ctx, events.NewNotification(ctx, slog.LevelWarn, fmt.Sprintf("有 %d 份媒体无法读取，本次请求已使用缺失提示；原始媒体引用仍保留。", len(missing))))
	}
	return resolved, release, nil
}

// MessageFromStorage is the sole projection of archived business messages.
func MessageFromStorage(row storage.Message) llm.LLMMessage {
	segments := MessageSegmentsFromStorage(row.Segments)
	metadata := AssistantMessageMetadata(row.Metadata)
	text := row.Content
	if row.Role == storage.RoleAssistant && metadata.RawText != "" {
		text = metadata.RawText
	}
	if len(segments) == 0 {
		segments = llm.TextSegments(text)
	}
	return llm.LLMMessage{Role: llm.MessageRole(row.Role), Segments: segments, Name: ToolNameFromMetadata(row.Metadata), ToolCallID: row.ToolCallID, ToolCalls: metadata.ToolCalls}
}

// MaterialMediaIDs collects retention references without opening media entities.
func MaterialMediaIDs(materials []Material, arguments []string, retained []string) []string {
	seen := map[string]bool{}
	ids := []string{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, id := range retained {
		add(id)
	}
	for _, material := range materials {
		for _, segment := range material.Segments {
			add(segment.MediaID)
		}
	}
	for _, argument := range arguments {
		for _, id := range media.ArgumentMediaIDs(argument) {
			add(id)
		}
	}
	return ids
}

func EncodeMaterials(materials []Material) (string, error) {
	if materials == nil {
		materials = []Material{}
	}
	data, err := json.Marshal(materials)
	return string(data), err
}

func DecodeMaterials(raw string) ([]Material, error) {
	var materials []Material
	if err := json.Unmarshal([]byte(raw), &materials); err != nil {
		return nil, err
	}
	for _, material := range materials {
		for _, segment := range material.Segments {
			if (segment.Type == llm.SegmentImage || segment.Type == llm.SegmentFile) && (segment.MediaID == "" || segment.URL != "") {
				return nil, fmt.Errorf("archived media requires a stable reference without an expanded URL")
			}
		}
	}
	return materials, nil
}
