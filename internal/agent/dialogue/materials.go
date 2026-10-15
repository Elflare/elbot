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
			imported, err := p.Media.Materialize(ctx, []llm.MessageSegment{segment})
			if err != nil {
				return nil, err
			}

			out[i].Segments[j] = imported[0]
		}
	}
	return out, ctx.Err()
}

// PrepareMaterials is the shared media preparation boundary for Chat and Responses.
// Callers supply all material in one actual request, including replay/continuation
// prefixes, so the media manager chooses one delivery policy for the whole request.
// Its release belongs to that request's lifecycle, including HTTP transport retries.
func (p *Preparer) PrepareMaterials(ctx context.Context, messages []llm.LLMMessage) ([]llm.LLMMessage, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, func() {}, err
	}
	resolved, release, err := p.Media.AcquireForLLM(ctx, messages)
	if err != nil {
		return nil, release, fmt.Errorf("准备模型媒体: %w", err)
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
func MessageFromStorage(row storage.Message) (llm.LLMMessage, error) {
	segments, err := MessageSegmentsFromStorage(row.Segments)
	if err != nil {
		return llm.LLMMessage{}, fmt.Errorf("message %s segments: %w", row.ID, err)
	}
	message := llm.LLMMessage{Role: llm.MessageRole(row.Role), ToolCallID: row.ToolCallID}
	text := row.Content
	switch row.Role {
	case storage.RoleAssistant:
		metadata, err := AssistantMessageMetadata(row.Metadata)
		if err != nil {
			return llm.LLMMessage{}, fmt.Errorf("message %s metadata: %w", row.ID, err)
		}
		if metadata.RawText != "" {
			text = metadata.RawText
		}
		message.ToolCalls = metadata.ToolCalls
	case storage.RoleTool:
		message.Name, err = ToolNameFromMetadata(row.Metadata)
		if err != nil {
			return llm.LLMMessage{}, fmt.Errorf("message %s metadata: %w", row.ID, err)
		}
	}
	if len(segments) == 0 {
		segments = llm.TextSegments(text)
	}
	message.Segments = segments
	return message, nil
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
