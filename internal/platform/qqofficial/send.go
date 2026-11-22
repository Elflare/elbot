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

// sendOutputs 按组准备内容，再逐页发送；上传素材不产生消息。
func (a *Adapter) sendOutputs(ctx context.Context, t sendTarget, outputs []delivery.Output) (delivery.Receipt, error) {
	if len(outputs) == 0 {
		return delivery.Receipt{}, nil
	}
	if t.Proactive && !a.cfg.allowProactive() {
		return delivery.Receipt{}, fmt.Errorf("qqofficial proactive messages are disabled")
	}
	nativeType := 0
	hasImage := false
	for _, out := range outputs {
		switch out.Kind {
		case delivery.KindImage, delivery.KindRecord, delivery.KindFile:
			hasImage = hasImage || out.Kind == delivery.KindImage
			if len(outputs) == 1 && out.Text == "" {
				nativeType = map[delivery.Kind]int{delivery.KindImage: fileTypeImage, delivery.KindRecord: 3, delivery.KindFile: fileTypeFile}[out.Kind]
			} else if out.Kind != delivery.KindImage {
				return delivery.Receipt{}, fmt.Errorf("qqofficial cannot combine %s with other content in one message", out.Kind)
			}
		}
	}
	var replyID string
	var msg messageToCreate
	pages := []outputPage{{}}
	page := &pages[0]
	newPage := func() { pages = append(pages, outputPage{}); page = &pages[len(pages)-1] }
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
		page.addMedia(0, outputs[0])
	default:
		mixed := hasImage
		first := outputs[0]
		// 暂无图+按钮，先简化处理：仅单段文本携带操作按钮。
		keyboard := len(outputs) == 1 && first.Kind == delivery.KindText && len(first.Actions) > 0 && a.cfg.markdownByDefault() && a.cfg.enableKeyboard()
		if keyboard {
			msg.Keyboard = commandKeyboard(a.cfg.AppID, first.Actions)
		}
		for i, out := range outputs {
			var text string
			switch out.Kind {
			case delivery.KindText:
				out.ShowActionHints = false
				text = delivery.OutputBody(out, keyboard)
			case delivery.KindReply:
				if t.Kind != targetGroup {
					text = delivery.FallbackText(out)
					break
				}
				if out.ReplyReferenceIndex == "" {
					return delivery.Receipt{}, fmt.Errorf("qqofficial group reply missing reference index")
				}
				if msg.MessageReference != nil && msg.MessageReference.MessageID != out.ReplyReferenceIndex {
					return delivery.Receipt{}, fmt.Errorf("qqofficial cannot combine different reply targets")
				}
				replyID = out.ReplyToPlatformMessageID
				msg.MessageReference = &messageReference{MessageID: out.ReplyReferenceIndex}
				text = delivery.OutputBody(out, false)
			case delivery.KindImage:
				text = out.Text
			default:
				text = delivery.FallbackText(out)
			}
			for text != "" {
				n := pagePrefix(text, pageBudget-page.size, mixed)
				if n > 0 {
					raw := text[:n]
					rendered := raw
					if mixed {
						rendered = escapeMarkdownText(raw)
					}
					page.append(rendered)
					page.history = append(page.history, storage.ChatSegment{Type: "text", Text: raw})
					text = text[n:]
				}
				if text != "" {
					newPage()
				}
			}
			if out.Kind != delivery.KindImage {
				continue
			}
			ref := out.Source.URL
			if ref == "" {
				var id string
				var err error
				ref, id, err = a.client.markdownImage(ctx, out.Source)
				if err != nil {
					return delivery.Receipt{}, err
				}
				defer a.client.releaseMarkdownImage(ctx, id)
			}
			image := "\n![](" + strings.NewReplacer("(", "%28", ")", "%29", " ", "%20").Replace(ref) + ")\n"
			size := utf8.RuneCountInString(image)
			if size > pageBudget {
				return delivery.Receipt{}, fmt.Errorf("qqofficial image reference exceeds %d characters", pageBudget)
			}
			if size+page.size > pageBudget {
				newPage()
			}
			page.append(image)
			page.addMedia(i, out)
		}
		if len(pages) == 1 && strings.TrimSpace(page.body) == "" {
			return delivery.Receipt{}, nil
		}
		if mixed || keyboard || (!t.commandReply && a.cfg.markdownByDefault()) {
			msg.MsgType = msgTypeMarkdown
		}
	}
	a.mu.Lock()
	botName := a.username
	a.mu.Unlock()
	var receipt delivery.Receipt
	chatType := qqOfficialChatType(t)
	for i, p := range pages {
		if len(pages) > 1 {
			suffix := fmt.Sprintf("\n（%d/%d）", i+1, len(pages))
			p.body += suffix
			p.history = append(p.history, storage.ChatSegment{Type: "text", Text: suffix})
		}
		if i > 0 {
			msg.Keyboard = nil
		}
		switch msg.MsgType {
		case msgTypeMarkdown:
			msg.Markdown = &messageMarkdown{Content: p.body, ForceVerifyImageResource: len(p.indexes) > 0}
		case msgTypeText:
			msg.Content = p.body
		}
		if msg.MsgID = strings.TrimSpace(t.MsgID); msg.MsgID != "" {
			msg.MsgSeq = a.nextMsgSeq(msg.MsgID)
		} else {
			msg.EventID = strings.TrimSpace(t.EventID)
		}
		resp, err := a.client.sendMessage(ctx, t, msg)
		if err != nil {
			return receipt, err
		}
		id := strings.TrimSpace(resp.ID)
		if id == "" {
			continue
		}
		platformshare.RecordAssistantMessage(ctx, a.chatHistory, storage.ChatMessage{Platform: a.Name(), ChatType: chatType, ChatID: t.OpenID, PlatformMessageID: id, SenderID: a.cfg.AppID, SenderName: botName, ReplyToPlatformMessageID: replyID}, p.history)
		a.recordMessageAlias(ctx, chatType, t.OpenID, resp.ExtInfo.RefIdx, id, a.cfg.AppID)
		receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, id)
		receipt.SentMessages = append(receipt.SentMessages, delivery.SentMessage{Platform: a.Name(), ChatType: chatType, ChatID: t.OpenID, PlatformMessageID: id, OutputIndexes: p.indexes})
	}
	return receipt, nil
}

const pageBudget = 1800 // 正文字符预算，剩余空间留给页码。
const markdownSpecials = "\\*_[]`#<>~"

type outputPage struct {
	body    string                // 已编码的 QQ 正文，不含页码。
	size    int                   // 正文 Unicode 字符数。
	history []storage.ChatSegment // 本页实际交付的原始片段。
	indexes []int                 // 本页媒体在整组 outputs 中的下标。
}

func (p *outputPage) append(s string) { p.body += s; p.size += utf8.RuneCountInString(s) }
func (p *outputPage) addMedia(i int, out delivery.Output) {
	seg := storage.ChatSegment{Type: string(out.Kind), Name: out.Name, MIMEType: out.Source.MIMEType}
	if out.Source.MediaID == "" {
		seg.URL = out.Source.URL
	}
	if out.Kind == delivery.KindRecord {
		seg = storage.ChatSegment{Type: "text", Text: "[语音]"}
	}
	p.history = append(p.history, seg)
	p.indexes = append(p.indexes, i)
}

// pagePrefix 按渲染后的字符预算切原文；换行归前页，Markdown 转义对不拆开。
func pagePrefix(text string, room int, escape bool) int {
	newline := 0
	for i, r := range text {
		room--
		if escape && strings.ContainsRune(markdownSpecials, r) {
			room--
		}
		if room < 0 {
			if newline > 0 {
				return newline
			}
			return i
		}
		if r == '\n' {
			newline = i + 1
		}
	}
	return len(text)
}
func escapeMarkdownText(text string) string {
	var b strings.Builder
	for _, r := range text {
		if strings.ContainsRune(markdownSpecials, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
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
