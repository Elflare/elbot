// send.go 处理发送目标、消息与媒体发送、分页及回执。
package telegram

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	platformshare "elbot/internal/platform/share"
	"elbot/internal/storage"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

func (a *Adapter) SendCommandResult(ctx context.Context, result command.Result, failure error) (delivery.Receipt, error) {
	outputs := platformshare.CommandResultOutputs(result, failure)
	return a.SendTo(ctx, delivery.Target{}, outputs)
}

type target struct {
	NativeChatID int64                // Telegram 原生聊天标识，群组允许负数。
	ChatType     contextinfo.ChatType // 会话类型，与原生 ID 共同标识会话。
	ChatID       string               // 回执记录使用的平台会话范围。
}

func (a *Adapter) SendTo(ctx context.Context, outTarget delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	ctx, finish, err := a.beginOperation(ctx)
	if err != nil {
		return delivery.Receipt{}, err
	}
	defer finish()
	if outTarget.Empty() {
		return a.sendContextOutput(ctx, outputs)
	}
	return a.sendTarget(ctx, outTarget, outputs)
}

func (a *Adapter) sendContextOutput(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	t, err := a.contextTarget(ctx)
	if err != nil {
		return delivery.Receipt{}, err
	}
	return a.sendOutputs(ctx, t, outputs)
}

func (a *Adapter) contextTarget(ctx context.Context) (target, error) {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.Platform != a.Name() {
		return target{}, fmt.Errorf("telegram send target missing")
	}
	return targetFromDelivery(delivery.Target{ChatType: info.Source.ChatType, ChatID: info.Source.ChatID})
}

func (a *Adapter) sendTarget(ctx context.Context, outTarget delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	if outTarget.Superadmins {
		return a.superadmins.SendTo(ctx, outputs, a.sendTarget)
	}
	t, err := targetFromDelivery(outTarget)
	if err != nil {
		return delivery.Receipt{}, err
	}
	return a.sendOutputs(ctx, t, outputs)
}

func (a *Adapter) sendOutputs(ctx context.Context, t target, outputs []delivery.Output) (delivery.Receipt, error) {
	var receipt delivery.Receipt
	for i, out := range outputs {
		sent, err := a.sendToTarget(ctx, t, out)
		var mediaIndexes []int
		if out.Kind == delivery.KindImage || out.Kind == delivery.KindFile {
			mediaIndexes = []int{i}
		}
		sent = a.telegramReceipt(sent, t, mediaIndexes...)
		receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
		receipt.SentMessages = append(receipt.SentMessages, sent.SentMessages...)
		if err != nil {
			return receipt, err
		}
	}
	return receipt, nil
}

// Every sent message has a source; only actual media outputs have media indexes.
func (a *Adapter) telegramReceipt(receipt delivery.Receipt, target target, mediaIndexes ...int) delivery.Receipt {
	scope := target.ChatID
	for _, id := range receipt.PlatformMessageIDs {
		if id = strings.TrimSpace(id); id != "" {
			receipt.SentMessages = append(receipt.SentMessages, delivery.SentMessage{PlatformMessageID: id, Platform: a.Name(), ChatType: target.ChatType, ChatID: scope, OutputIndexes: append([]int(nil), mediaIndexes...)})
		}
	}
	return receipt
}
func (a *Adapter) sendToTarget(ctx context.Context, t target, out delivery.Output) (delivery.Receipt, error) {
	var msg sentMessage
	var err error
	seg := storage.ChatSegment{Type: string(out.Kind), Name: out.Name, MIMEType: out.Source.MIMEType}
	switch out.Kind {
	case delivery.KindText:
		out.ShowActionHints = false
		return a.sendText(ctx, t, delivery.OutputBody(out, true), 0, out.Actions)
	case delivery.KindReply:
		replyID, err := strconv.ParseInt(strings.TrimSpace(out.ReplyToPlatformMessageID), 10, 64)
		if err != nil {
			return delivery.Receipt{}, err
		}
		return a.sendText(ctx, t, out.Text, replyID, out.Actions)
	case delivery.KindEmoticon:
		msg, err = a.client.sendSticker(ctx, t.NativeChatID, out.EmoticonID, 0)
		seg = storage.ChatSegment{Type: "text", Text: delivery.FallbackText(out)}
	case delivery.KindImage:
		msg, err = a.client.sendPhoto(ctx, t.NativeChatID, sourceFromOutput(out), 0)
	case delivery.KindFile:
		msg, err = a.client.sendDocument(ctx, t.NativeChatID, sourceFromOutput(out), 0)
	default:
		return a.sendText(ctx, t, delivery.FallbackText(out), 0, nil)
	}
	if err != nil {
		return delivery.Receipt{}, err
	}
	var receipt delivery.Receipt
	a.recordOutput(ctx, t, &receipt, msg, 0, seg)
	return receipt, nil
}

func (a *Adapter) sendText(ctx context.Context, t target, text string, replyTo int64, actions []delivery.Action) (delivery.Receipt, error) {
	text, actions = keyboardActions(text, actions)
	text = strings.TrimSpace(text)
	if text == "" {
		return delivery.Receipt{}, nil
	}
	switch a.cfg.Format {
	case "rich":
		return a.sendRichText(ctx, t, text, replyTo, actions)
	case "plain":
		return a.sendPlainText(ctx, t, text, replyTo, actions)
	default:
		return a.sendHTMLText(ctx, t, text, replyTo, actions)
	}
}

func (a *Adapter) sendRichText(ctx context.Context, t target, text string, replyTo int64, actions []delivery.Action) (delivery.Receipt, error) {
	pages := telegramRichTextPages(text)
	var receipt delivery.Receipt
	for i, page := range pages {
		var pageReply int64
		req := sendRichMessageRequest{ChatID: t.NativeChatID, RichMessage: inputRichMessage{Markdown: page}}
		if i == 0 {
			if replyTo != 0 {
				pageReply = replyTo
				req.ReplyParameters = &replyParameters{MessageID: pageReply}
			}
			if len(actions) > 0 {
				req.ReplyMarkup = commandKeyboard(actions)
			}
		}
		msg, err := a.client.sendRichMessage(ctx, req)
		if err != nil {
			return receipt, err
		}
		a.recordOutput(ctx, t, &receipt, msg, pageReply, storage.ChatSegment{Type: "text", Text: page})
	}
	return receipt, nil
}

func (a *Adapter) sendHTMLText(ctx context.Context, t target, text string, replyTo int64, actions []delivery.Action) (delivery.Receipt, error) {
	return a.sendFormattedText(ctx, t, telegramHTMLFromMarkdown(text), "HTML", replyTo, actions)
}

func (a *Adapter) sendPlainText(ctx context.Context, t target, text string, replyTo int64, actions []delivery.Action) (delivery.Receipt, error) {
	return a.sendFormattedText(ctx, t, plainTextFromMarkdown(text), "", replyTo, actions)
}

func (a *Adapter) sendFormattedText(ctx context.Context, t target, text, parseMode string, replyTo int64, actions []delivery.Action) (delivery.Receipt, error) {
	pages := telegramTextPages(text)
	var receipt delivery.Receipt
	for i, page := range pages {
		req := sendMessageRequest{ChatID: t.NativeChatID, Text: page, ParseMode: parseMode}
		if i == 0 {
			req.ReplyToMessageID = replyTo
			if len(actions) > 0 {
				req.ReplyMarkup = commandKeyboard(actions)
			}
		}
		msg, err := a.client.sendMessage(ctx, req)
		if err != nil {
			return receipt, err
		}
		a.recordOutput(ctx, t, &receipt, msg, req.ReplyToMessageID, storage.ChatSegment{Type: "text", Text: page})
	}
	return receipt, nil
}

// recordOutput 接收单页或单个附件的回执；协议返回的正文优先于提交的文本。
func (a *Adapter) recordOutput(ctx context.Context, t target, receipt *delivery.Receipt, msg sentMessage, replyTo int64, seg storage.ChatSegment) bool {
	if msg.MessageID == 0 {
		return false
	}
	id := formatMessageID(msg.MessageID)
	receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, id)
	if msg.Text != "" {
		seg.Text = msg.Text
	}
	if len(msg.Photo) > 0 {
		photo := largestPhoto(msg.Photo)
		seg.PlatformFileID, seg.Size = photo.FileID, photo.FileSize
	}
	if d := msg.Document; d != nil {
		seg.PlatformFileID, seg.Name, seg.MIMEType, seg.Size = d.FileID, d.FileName, d.MIMEType, d.FileSize
	}
	a.mu.Lock()
	bot := a.bot
	a.mu.Unlock()
	platformshare.RecordAssistantMessage(ctx, a.chatHistory, storage.ChatMessage{Platform: a.Name(), ChatType: t.ChatType, ChatID: t.ChatID, PlatformMessageID: id, SenderID: bot.UserID, SenderName: bot.Username, ReplyToPlatformMessageID: formatMessageID(replyTo)}, []storage.ChatSegment{seg})
	return true
}

func targetFromDelivery(outTarget delivery.Target) (target, error) {
	switch outTarget.ChatType {
	case contextinfo.ChatPrivate, contextinfo.ChatGroup, contextinfo.ChatChannel:
	default:
		return target{}, fmt.Errorf("telegram invalid conversation kind %q", outTarget.ChatType)
	}
	id, err := strconv.ParseInt(outTarget.ChatID, 10, 64)
	if err != nil {
		return target{}, fmt.Errorf("parse telegram chat id: %w", err)
	}
	return target{NativeChatID: id, ChatType: outTarget.ChatType, ChatID: outTarget.ChatID}, nil
}

func sourceFromOutput(out delivery.Output) mediaSource {
	return mediaSource{URL: out.Source.URL, Path: out.Source.Path, Data: out.Source.Data, Name: mediaSourceName(out)}
}

func mediaSourceName(out delivery.Output) string {
	if name := strings.TrimSpace(out.Name); name != "" {
		return name
	}
	for _, value := range []string{out.Source.Path, out.Source.URL} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if name := strings.TrimSpace(filepath.Base(value)); name != "" && name != "." && name != string(filepath.Separator) {
			return name
		}
	}
	return "file"
}

func telegramRichTextPages(text string) []string {
	return telegramTextPagesByLimit(text, telegramRichTextRunes)
}

func telegramTextPages(text string) []string {
	return telegramTextPagesByLimit(text, telegramTextPageRunes)
}

func telegramTextPagesByLimit(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	pageSize := limit - 16
	total := (len(runes) + pageSize - 1) / pageSize
	pages := make([]string, 0, total)
	for start := 0; start < len(runes); start += pageSize {
		end := start + pageSize
		if end > len(runes) {
			end = len(runes)
		}
		pages = append(pages, fmt.Sprintf("%s\n（%d/%d）", string(runes[start:end]), len(pages)+1, total))
	}
	return pages
}
