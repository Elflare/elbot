// reference.go 解析引用索引、恢复事件自带引用内容，并登记或查询消息别名关联。

package qqofficial

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/platform/share"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	globalevents "elbot/internal/events"
	"elbot/internal/storage"
)

func inboundReferenceIndexes(msg inboundMessage) (msgIndex, refIndex string) {
	for _, entry := range msg.MessageScene.Ext {
		key, value, ok := strings.Cut(entry, "=")
		value = strings.TrimSpace(value)
		if !ok || value == "" {
			continue
		}
		switch strings.TrimSpace(key) {
		case "msg_idx":
			msgIndex = value
		case "ref_msg_idx":
			refIndex = value
		}
	}
	return msgIndex, refIndex
}

func (a *Adapter) resolveInboundReplyID(ctx context.Context, conversationKind contextinfo.ChatType, scopeID, refIndex string) (string, string, error) {
	if refIndex == "" {
		return "", "", nil
	}
	id, senderID, err := a.chatHistory.ResolvePlatformMessageAlias(ctx, a.Name(), conversationKind, scopeID, refIndex)
	if errors.Is(err, storage.ErrNotFound) {
		return "", "", nil
	}
	return id, senderID, err
}

func inboundReferencedMessage(msg inboundMessage, refIndex string) share.ReferencedMessage {
	if (refIndex == "" && msg.MessageType != 103) || len(msg.MsgElements) == 0 {
		return share.ReferencedMessage{}
	}
	var segments []share.MessageSegment
	seenText := false
	for _, element := range msg.MsgElements {
		text := normalizedInboundText(element.Content)
		if text != "" {
			if seenText {
				text = "\n" + text
			}
			seenText = true
		}
		segments = append(segments, inboundSegments(text, inboundAttachmentSegments(element.Attachments))...)
	}
	return share.ReferencedMessage{Text: share.SegmentsText(segments), Segments: segments}
}

func (a *Adapter) recordMessageAlias(ctx context.Context, conversationKind contextinfo.ChatType, scopeID, alias, messageID, senderID string) {
	alias, messageID = strings.TrimSpace(alias), strings.TrimSpace(messageID)
	if alias == "" || messageID == "" {
		return
	}
	if err := a.chatHistory.MapPlatformMessageAlias(ctx, storage.PlatformMessageAlias{
		Platform: a.Name(), ChatType: conversationKind, ChatID: scopeID, Alias: alias, PlatformMessageID: messageID, SenderID: senderID,
	}); err != nil {
		globalevents.Log(context.WithoutCancel(ctx), globalevents.LogRecord{
			Category: globalevents.LogCategoryLog, Level: slog.LevelWarn,
			EventName: "record_qqofficial_message_alias_failed", Module: a.Name(),
			Type: globalevents.ContentText, ContentLimit: 4096, ResultStatus: globalevents.ResultFailed,
			Content: fmt.Sprintf("record qqofficial message alias failed: %v", err),
		})
	}
}
