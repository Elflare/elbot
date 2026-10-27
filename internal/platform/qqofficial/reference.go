// reference.go 解析引用索引、恢复事件自带引用内容，并登记或查询消息别名关联。

package qqofficial

import (
	"context"
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
	if msg.MessageType == 103 {
		for _, element := range msg.MsgElements {
			if index := strings.TrimSpace(element.MsgIdx); index != "" {
				refIndex = index
				break
			}
		}
	}
	return msgIndex, refIndex
}

func (a *Adapter) resolveInboundReplyID(ctx context.Context, scopeID, refIndex string) (string, error) {
	if refIndex == "" {
		return "", nil
	}
	id, err := a.chatHistory.ResolvePlatformMessageAlias(ctx, a.Name(), scopeID, refIndex)
	if errors.Is(err, storage.ErrNotFound) {
		return "", nil
	}
	return id, err
}

func inboundReferencedMessage(msg inboundMessage, refIndex string) share.ReferencedMessage {
	if (refIndex == "" && msg.MessageType != 103) || len(msg.MsgElements) == 0 {
		return share.ReferencedMessage{}
	}
	element := msg.MsgElements[0]
	text := normalizedInboundText(inboundMessage{Content: element.Content}, false)
	segments := inboundSegments(text, inboundAttachmentSegments(element.Attachments))
	if text == "" && len(segments) > 0 {
		text = "[媒体消息]"
	}
	return share.ReferencedMessage{Text: text, Segments: segments}
}

func (a *Adapter) recordMessageAlias(ctx context.Context, scopeID, alias, messageID string) {
	alias, messageID = strings.TrimSpace(alias), strings.TrimSpace(messageID)
	if alias == "" || messageID == "" {
		return
	}
	if err := a.chatHistory.MapPlatformMessageAlias(ctx, storage.PlatformMessageAlias{
		Platform: a.Name(), PlatformScopeID: scopeID, Alias: alias, PlatformMessageID: messageID,
	}); err != nil {
		_ = globalevents.EmitLog(context.WithoutCancel(ctx), globalevents.LogRecord{
			Category: globalevents.LogRuntime, Level: slog.LevelWarn,
			EventName: "record_qqofficial_message_alias_failed", Module: "qqofficial",
			Type: globalevents.ContentText, ContentLimit: 4096, ResultStatus: globalevents.ResultFailed,
			Content: fmt.Sprintf("record qqofficial message alias failed: %v", err),
		})
	}
}
