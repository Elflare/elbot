// send.go 负责消息编码、媒体上传、实际发送及回执整理。

package qqofficial

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	platformshare "elbot/internal/platform/share"
	"elbot/internal/storage"
)

func (a *Adapter) SendCommandResult(ctx context.Context, result command.Result, failure error) (delivery.Receipt, error) {
	outputs := platformshare.CommandResultOutputs(result, failure)
	return a.sendContextOutput(ctx, outputs, true)
}

const (
	msgTypeText     = 0
	msgTypeMarkdown = 2
	msgTypeMedia    = 7

	fileTypeImage = 1
	fileTypeFile  = 4
)

func (a *Adapter) sendContextOutput(ctx context.Context, outputs []delivery.Output, commandReply bool) (delivery.Receipt, error) {
	ctx, finish, err := a.beginSend(ctx)
	if err != nil {
		return delivery.Receipt{}, err
	}
	defer finish()
	t, err := a.contextTarget(ctx)
	if err != nil {
		return delivery.Receipt{}, err
	}
	t.commandReply = commandReply
	return a.sendOutputs(ctx, t, outputs)
}

// sendOutputs 将一次投递编码成一条平台消息；上传素材不产生消息。
func (a *Adapter) sendOutputs(ctx context.Context, t sendTarget, outputs []delivery.Output) (delivery.Receipt, error) {
	if len(outputs) == 0 {
		return delivery.Receipt{}, nil
	}
	if t.Proactive && !a.cfg.allowProactive() {
		return delivery.Receipt{}, fmt.Errorf("qqofficial proactive messages are disabled")
	}
	recordHistory := t.commandReply
	var indexes []int
	nativeType := 0
	for i, out := range outputs {
		if out.Delivery.HookPoint != "" {
			recordHistory = true
		}
		switch out.Kind {
		case delivery.KindImage, delivery.KindRecord, delivery.KindFile:
			if len(outputs) == 1 && out.Text == "" {
				nativeType = map[delivery.Kind]int{delivery.KindImage: fileTypeImage, delivery.KindRecord: 3, delivery.KindFile: fileTypeFile}[out.Kind]
			} else if out.Kind != delivery.KindImage {
				return delivery.Receipt{}, fmt.Errorf("qqofficial cannot combine %s with other content in one message", out.Kind)
			}
			indexes = append(indexes, i)
		}
	}
	var msg messageToCreate
	text := make([]string, len(outputs))
	switch {
	case nativeType != 0:
		src := outputs[0].Source
		source, err := prepareSource(src.URL, src.Path, src.Data)
		if err != nil {
			return delivery.Receipt{}, err
		}
		uploaded, err := a.client.uploadFile(ctx, t, nativeType, source)
		if err != nil {
			return delivery.Receipt{}, err
		}
		msg.MsgType = msgTypeMedia
		msg.Media = &messageMedia{FileInfo: uploaded.FileInfo}
	default:
		mixed := len(indexes) > 0
		first := outputs[0]
		// 暂无图+按钮，先简化处理：仅单段文本携带操作按钮。
		keyboard := len(outputs) == 1 && first.Kind == delivery.KindText && len(first.Actions) > 0 && a.cfg.markdownByDefault() && a.cfg.enableKeyboard()
		if keyboard {
			msg.Keyboard = commandKeyboard(a.cfg.AppID, first.Actions)
		}
		parts := make([]string, len(outputs))
		imagePart := func(i int, ref string) string {
			return escapeMarkdownText(text[i]) + "\n![](" + strings.NewReplacer("(", "%28", ")", "%29", " ", "%20").Replace(ref) + ")\n"
		}
		var localImages []int
		for i, out := range outputs {
			switch out.Kind {
			case delivery.KindImage:
				text[i] = out.Text
				parts[i] = imagePart(i, out.Source.URL)
				if out.Source.URL == "" {
					localImages = append(localImages, i)
				}
				continue
			case delivery.KindText:
				out.ShowActionHints = false
				text[i] = delivery.OutputBody(out, keyboard)
			default:
				text[i] = delivery.FallbackText(out)
			}
			parts[i] = text[i]
			if mixed {
				parts[i] = escapeMarkdownText(text[i])
			}
		}
		// 本地引用暂为空：已知部分超限时，无需上传。
		body := strings.TrimSpace(strings.Join(parts, ""))
		const limit = 2000
		tooLong := fmt.Errorf("qqofficial message exceeds %d characters", limit)
		if utf8.RuneCountInString(body) > limit {
			return delivery.Receipt{}, tooLong
		}
		if body == "" {
			return delivery.Receipt{}, nil
		}
		for _, i := range localImages {
			ref, id, err := a.client.markdownImage(ctx, outputs[i].Source)
			if err != nil {
				return delivery.Receipt{}, err
			}
			defer a.client.releaseMarkdownImage(ctx, id)
			parts[i] = imagePart(i, ref)
		}
		if len(localImages) > 0 {
			body = strings.TrimSpace(strings.Join(parts, ""))
			if utf8.RuneCountInString(body) > limit {
				return delivery.Receipt{}, tooLong
			}
		}
		if mixed || keyboard || (!t.commandReply && a.cfg.markdownByDefault()) {
			msg.MsgType = msgTypeMarkdown
			msg.Markdown = &messageMarkdown{Content: body, ForceVerifyImageResource: mixed}
		} else {
			msg.Content = body
		}
	}
	if msg.MsgID = strings.TrimSpace(t.MsgID); msg.MsgID != "" {
		msg.MsgSeq = a.nextMsgSeq(msg.MsgID)
	} else {
		msg.EventID = strings.TrimSpace(t.EventID)
	}
	resp, err := a.client.sendMessage(ctx, t, msg)
	if err != nil {
		return delivery.Receipt{}, err
	}
	id := strings.TrimSpace(resp.ID)
	if id == "" {
		return delivery.Receipt{}, nil
	}
	if t.Kind == targetGroup && recordHistory {
		a.recordGroupOutput(ctx, t, id, text, outputs)
	}
	chatType := qqOfficialChatType(t)
	a.recordMessageAlias(ctx, chatType, t.OpenID, resp.ExtInfo.RefIdx, id)
	return delivery.Receipt{PlatformMessageIDs: []string{id}, SentMessages: []delivery.SentMessage{{Platform: a.Name(), ChatType: chatType, ChatID: t.OpenID, PlatformMessageID: id, OutputIndexes: indexes}}}, nil
}

func escapeMarkdownText(text string) string {
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "`", "\\`", "#", "\\#", "<", "\\<", ">", "\\>", "~", "\\~").Replace(text)
}

func qqOfficialChatType(target sendTarget) contextinfo.ChatType {
	if target.Kind == targetGroup {
		return contextinfo.ChatGroup
	}
	return contextinfo.ChatPrivate
}

func (a *Adapter) contextTarget(ctx context.Context) (sendTarget, error) {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.Platform != a.Name() {
		return sendTarget{}, fmt.Errorf("qqofficial send target missing")
	}
	out := delivery.Target{ChatType: info.Source.ChatType, ChatID: info.Source.ChatID}
	t, err := a.targetFromDelivery(out)
	if err != nil {
		return sendTarget{}, err
	}
	t.MsgID = info.PlatformMessageID
	// Ordinary messages reply with msg_id. Event-only sources use event_id.
	if data, ok := info.PlatformData.(messageData); ok && t.MsgID == "" {
		t.EventID = data.EventID
	}
	return t, nil
}

func (a *Adapter) recordGroupOutput(ctx context.Context, target sendTarget, id string, parts []string, outputs []delivery.Output) {
	segments := make([]storage.ChatSegment, len(outputs))
	for i, out := range outputs {
		seg := storage.ChatSegment{Type: "text", Text: parts[i]}
		switch out.Kind {
		case delivery.KindImage, delivery.KindFile, delivery.KindRecord:
			seg.Type, seg.Name, seg.MIMEType = string(out.Kind), out.Name, out.Source.MIMEType
			// 不持久化媒体中心的签名地址。
			if out.Source.MediaID == "" {
				seg.URL = out.Source.URL
			}
		}
		segments[i] = seg
	}
	text := strings.Join(parts, "")
	a.appendChatMessage(ctx, &storage.ChatMessage{
		Platform: a.Name(), ChatType: contextinfo.ChatGroup, ChatID: target.OpenID,
		PlatformMessageID: id, SenderID: a.cfg.AppID, SenderName: "bot",
		Text: text, Raw: text, Segments: storage.MarshalChatSegments(segments), CreatedAt: storage.Now(),
	})
}
func (a *Adapter) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	if target.Empty() {
		return a.sendContextOutput(ctx, outputs, false)
	}
	ctx, finish, err := a.beginSend(ctx)
	if err != nil {
		return delivery.Receipt{}, err
	}
	defer finish()
	if target.Superadmins {
		if name := strings.TrimSpace(target.Platform); name != "" && name != a.Name() {
			err := fmt.Errorf("qqofficial cannot send to platform %q", name)
			return delivery.Receipt{}, err
		}
		return a.superadmins.SendTo(ctx, outputs, a.sendTarget)
	}
	return a.sendTarget(ctx, target, outputs)
}

func (a *Adapter) sendTarget(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	t, err := a.targetFromDelivery(target)
	if err != nil {
		return delivery.Receipt{}, err
	}
	t.Proactive = true
	return a.sendOutputs(ctx, t, outputs)
}

func (a *Adapter) targetFromDelivery(target delivery.Target) (sendTarget, error) {
	if platformName := strings.TrimSpace(target.Platform); platformName != "" && platformName != a.Name() {
		return sendTarget{}, fmt.Errorf("qqofficial cannot send to platform %q", platformName)
	}

	switch target.ChatType {
	case contextinfo.ChatPrivate:
		return sendTarget{Kind: targetC2C, OpenID: target.ChatID}, nil
	case contextinfo.ChatGroup:
		return sendTarget{Kind: targetGroup, OpenID: target.ChatID}, nil
	default:
		return sendTarget{}, fmt.Errorf("qqofficial invalid conversation kind %q", target.ChatType)
	}

}

func (a *Adapter) nextMsgSeq(msgID string) int {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return 0
	}
	a.seqMu.Lock()
	defer a.seqMu.Unlock()
	a.seqByID[msgID]++
	return a.seqByID[msgID]
}

type sendTarget struct {
	commandReply bool           // 命令回复默认纯文本；群内同时保存实发消息供引用。
	Kind         sendTargetKind // 私聊或群聊目标。
	OpenID       string         // 对应用户或群的开放标识。
	MsgID        string         // 被回复的平台消息 ID，用于被动发送及序号分配。
	EventID      string         // 无消息 ID 时使用的事件回复标识。
	Proactive    bool           // 是否属于显式目标的主动发送。
}

type sendTargetKind string

const (
	targetC2C   sendTargetKind = "c2c"
	targetGroup sendTargetKind = "group"
)
