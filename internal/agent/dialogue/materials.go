package dialogue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"elbot/internal/events"
	"elbot/internal/llm"
)

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
