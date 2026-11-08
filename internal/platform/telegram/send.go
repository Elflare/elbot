// send.go 处理发送目标、消息与媒体发送、分页及回执。
package telegram

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	platformshare "elbot/internal/platform/share"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

func (a *Adapter) SendCommandResult(ctx context.Context, req command.Request, result *command.Result, failure *command.Error) (delivery.Receipt, error) {
	var outputs []delivery.Output
	if result != nil {
		out := delivery.Text(platformshare.FormatCommandResult(req, result))
		if result.Confirmation != nil {
			out.Actions = result.Confirmation.Actions
		}
		outputs = append(outputs, out)
	}
	if failure != nil {
		outputs = append(outputs, delivery.Text(failure.Message))
	}
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
	switch out.Kind {
	case delivery.KindText:
		return a.sendText(ctx, t, out.Text, 0, out.Actions)
	case delivery.KindReply:
		replyID, err := strconv.ParseInt(strings.TrimSpace(out.ReplyToPlatformMessageID), 10, 64)
		if err != nil {
			return delivery.Receipt{}, err
		}
		return a.sendText(ctx, t, out.Text, replyID, out.Actions)
	case delivery.KindEmoticon:
		return a.sendSticker(ctx, t, out, 0)
	case delivery.KindImage:
		return a.sendPhoto(ctx, t, out, 0)
	case delivery.KindFile:
		return a.sendDocument(ctx, t, out, 0)
	default:
		return a.sendText(ctx, t, delivery.FallbackText(out), 0, nil)
	}
}

func (a *Adapter) sendText(ctx context.Context, t target, text string, replyTo int64, actions []delivery.Action) (delivery.Receipt, error) {
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
		req := sendRichMessageRequest{ChatID: t.NativeChatID, RichMessage: inputRichMessage{Markdown: page}}
		if i == 0 {
			if replyTo != 0 {
				req.ReplyParameters = &replyParameters{MessageID: replyTo}
			}
			if len(actions) > 0 {
				req.ReplyMarkup = commandKeyboard(actions)
			}
		}
		msg, err := a.client.sendRichMessage(ctx, req)
		if err != nil {
			return receipt, err
		}
		if msg.MessageID != 0 {
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, formatMessageID(msg.MessageID))
		}
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
		if msg.MessageID != 0 {
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, formatMessageID(msg.MessageID))
		}
	}
	return receipt, nil
}

func (a *Adapter) sendPhoto(ctx context.Context, t target, out delivery.Output, replyTo int64) (delivery.Receipt, error) {
	msg, err := a.client.sendPhoto(ctx, t.NativeChatID, sourceFromOutput(out), replyTo)
	return receiptWithMessageID(msg.MessageID), err
}

func (a *Adapter) sendDocument(ctx context.Context, t target, out delivery.Output, replyTo int64) (delivery.Receipt, error) {
	msg, err := a.client.sendDocument(ctx, t.NativeChatID, sourceFromOutput(out), replyTo)
	return receiptWithMessageID(msg.MessageID), err
}

func (a *Adapter) sendSticker(ctx context.Context, t target, out delivery.Output, replyTo int64) (delivery.Receipt, error) {
	msg, err := a.client.sendSticker(ctx, t.NativeChatID, out.EmoticonID, replyTo)
	return receiptWithMessageID(msg.MessageID), err
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

func receiptWithMessageID(id int64) delivery.Receipt {
	if id == 0 {
		return delivery.Receipt{}
	}
	return delivery.Receipt{PlatformMessageIDs: []string{formatMessageID(id)}}
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
