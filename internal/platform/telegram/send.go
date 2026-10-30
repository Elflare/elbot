// send.go 处理发送目标、消息与媒体发送、分页及回执。
package telegram

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

type target struct {
	ChatID  int64  // Telegram 原生聊天标识，群组允许负数。
	ScopeID string // 回执记录使用的平台会话范围。
}

func (a *Adapter) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	ctx, finish, err := a.beginOperation(ctx)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	defer finish()
	return a.sendContextOutput(ctx, outputs)
}

func (a *Adapter) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	outTarget := target
	if outTarget.Empty() {
		return a.SendChat(ctx, outputs)
	}
	ctx, finish, err := a.beginOperation(ctx)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	defer finish()
	return a.sendTarget(ctx, outTarget, outputs)
}

func (a *Adapter) sendContextOutput(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	t, err := contextTarget(ctx)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		return delivery.Receipt{}, logResult, err
	}
	return a.sendOutputs(ctx, t, outputs)
}

func contextTarget(ctx context.Context) (target, error) {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.Platform != platformName {
		return target{}, fmt.Errorf("telegram send target missing")
	}
	t, err := targetFromDelivery(delivery.Target{ScopeID: info.Source.ScopeID})
	if info.Source.ConversationID != "" {
		id, parseErr := strconv.ParseInt(info.Source.ConversationID, 10, 64)
		if parseErr != nil || id == 0 {
			return target{}, fmt.Errorf("telegram invalid conversation ID %q", info.Source.ConversationID)
		}
		t = target{ChatID: id, ScopeID: info.Source.ScopeID}
		if t.ScopeID == "" {
			switch info.Source.ConversationKind {
			case contextinfo.ConversationPrivate, contextinfo.ConversationGroup, contextinfo.ConversationChannel:
				t.ScopeID = string(info.Source.ConversationKind) + ":" + strconv.FormatInt(id, 10)
			}
		}
		err = nil
	}
	return t, err
}

func (a *Adapter) sendTarget(ctx context.Context, outTarget delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	if outTarget.Superadmins {
		if len(a.cfg.Superadmins) == 0 {
			{
				err := fmt.Errorf("telegram superadmins are not configured")
				logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
				return delivery.Receipt{}, logResult, err
			}
		}
		var receipt delivery.Receipt
		for _, id := range a.cfg.Superadmins {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			copyTarget := outTarget
			copyTarget.Superadmins = false
			copyTarget.PrivateUserID = id
			copyTarget.GroupID = ""
			copyTarget.ScopeID = ""
			sent, logResult, err := a.sendTarget(ctx, copyTarget, outputs)
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
			receipt.SentMessages = append(receipt.SentMessages, sent.SentMessages...)
			if err != nil {
				return receipt, logResult, err
			}
		}
		return receipt, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}
	t, err := targetFromDelivery(outTarget)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		return delivery.Receipt{}, logResult, err
	}
	return a.sendOutputs(ctx, t, outputs)
}

func (a *Adapter) sendOutputs(ctx context.Context, t target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	var receipt delivery.Receipt
	for i, out := range outputs {
		sent, logResult, err := a.sendToTarget(ctx, t, out)
		var mediaIndexes []int
		if out.Kind == delivery.KindImage || out.Kind == delivery.KindFile {
			mediaIndexes = []int{i}
		}
		sent = telegramReceipt(sent, t, mediaIndexes...)
		receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
		receipt.SentMessages = append(receipt.SentMessages, sent.SentMessages...)
		if err != nil {
			return receipt, logResult, err
		}
	}
	return receipt, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

// Every sent message has a source; only actual media outputs have media indexes.
func telegramReceipt(receipt delivery.Receipt, target target, mediaIndexes ...int) delivery.Receipt {
	scope := strings.TrimSpace(target.ScopeID)
	for _, id := range receipt.PlatformMessageIDs {
		if id = strings.TrimSpace(id); id != "" {
			receipt.SentMessages = append(receipt.SentMessages, delivery.SentMessage{PlatformMessageID: id, Platform: platformName, ScopeID: scope, OutputIndexes: append([]int(nil), mediaIndexes...)})
		}
	}
	return receipt
}
func (a *Adapter) sendToTarget(ctx context.Context, t target, out delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	switch out.Kind {
	case delivery.KindText:
		return a.sendText(ctx, t, out.Text, 0, out.Actions)
	case delivery.KindReply:
		replyID, err := strconv.ParseInt(strings.TrimSpace(out.ReplyToPlatformMessageID), 10, 64)
		if err != nil {
			return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
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

func (a *Adapter) sendText(ctx context.Context, t target, text string, replyTo int64, actions []delivery.Action) (delivery.Receipt, globalevents.LogResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
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

func (a *Adapter) sendRichText(ctx context.Context, t target, text string, replyTo int64, actions []delivery.Action) (delivery.Receipt, globalevents.LogResult, error) {
	pages := telegramRichTextPages(text)
	var receipt delivery.Receipt
	for i, page := range pages {
		req := sendRichMessageRequest{ChatID: t.ChatID, RichMessage: inputRichMessage{Markdown: page}}
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
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return receipt, logResult, err
		}
		if msg.MessageID != 0 {
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, formatMessageID(msg.MessageID))
		}
	}
	return receipt, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (a *Adapter) sendHTMLText(ctx context.Context, t target, text string, replyTo int64, actions []delivery.Action) (delivery.Receipt, globalevents.LogResult, error) {
	return a.sendFormattedText(ctx, t, telegramHTMLFromMarkdown(text), "HTML", replyTo, actions)
}

func (a *Adapter) sendPlainText(ctx context.Context, t target, text string, replyTo int64, actions []delivery.Action) (delivery.Receipt, globalevents.LogResult, error) {
	return a.sendFormattedText(ctx, t, plainTextFromMarkdown(text), "", replyTo, actions)
}

func (a *Adapter) sendFormattedText(ctx context.Context, t target, text, parseMode string, replyTo int64, actions []delivery.Action) (delivery.Receipt, globalevents.LogResult, error) {
	pages := telegramTextPages(text)
	var receipt delivery.Receipt
	for i, page := range pages {
		req := sendMessageRequest{ChatID: t.ChatID, Text: page, ParseMode: parseMode}
		if i == 0 {
			req.ReplyToMessageID = replyTo
			if len(actions) > 0 {
				req.ReplyMarkup = commandKeyboard(actions)
			}
		}
		msg, err := a.client.sendMessage(ctx, req)
		if err != nil {
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return receipt, logResult, err
		}
		if msg.MessageID != 0 {
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, formatMessageID(msg.MessageID))
		}
	}
	return receipt, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (a *Adapter) sendPhoto(ctx context.Context, t target, out delivery.Output, replyTo int64) (delivery.Receipt, globalevents.LogResult, error) {
	msg, err := a.client.sendPhoto(ctx, t.ChatID, sourceFromOutput(out), replyTo)
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
	if err != nil {
		logResult.ResultStatus = globalevents.ResultFailed
		logResult.Content = err.Error()
	}
	return receiptWithMessageID(msg.MessageID), logResult, err
}

func (a *Adapter) sendDocument(ctx context.Context, t target, out delivery.Output, replyTo int64) (delivery.Receipt, globalevents.LogResult, error) {
	msg, err := a.client.sendDocument(ctx, t.ChatID, sourceFromOutput(out), replyTo)
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
	if err != nil {
		logResult.ResultStatus = globalevents.ResultFailed
		logResult.Content = err.Error()
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
	}
	return receiptWithMessageID(msg.MessageID), logResult, err
}

func (a *Adapter) sendSticker(ctx context.Context, t target, out delivery.Output, replyTo int64) (delivery.Receipt, globalevents.LogResult, error) {
	msg, err := a.client.sendSticker(ctx, t.ChatID, out.EmoticonID, replyTo)
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
	if err != nil {
		logResult.ResultStatus = globalevents.ResultFailed
		logResult.Content = err.Error()
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
	}
	return receiptWithMessageID(msg.MessageID), logResult, err
}

func targetFromDelivery(outTarget delivery.Target) (target, error) {
	if strings.TrimSpace(outTarget.PrivateUserID) == "" && strings.TrimSpace(outTarget.GroupID) == "" {
		scope := strings.TrimSpace(outTarget.ScopeID)
		for _, prefix := range []string{"private:", "group:", "supergroup:"} {
			if strings.HasPrefix(scope, prefix) {
				if prefix == "private:" {
					outTarget.PrivateUserID = strings.TrimPrefix(scope, prefix)
				} else {
					outTarget.GroupID = strings.TrimPrefix(scope, prefix)
				}
				break
			}
		}
	}
	idText := firstNonEmpty(outTarget.PrivateUserID, outTarget.GroupID)
	if strings.TrimSpace(idText) == "" {
		return target{}, fmt.Errorf("telegram target missing private_user_id, group_id or scope_id")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(idText), 10, 64)
	if err != nil {
		return target{}, fmt.Errorf("parse telegram chat id: %w", err)
	}
	scope := strings.TrimSpace(outTarget.ScopeID)
	if scope == "" {
		if strings.TrimSpace(outTarget.PrivateUserID) != "" {
			scope = "private:" + strconv.FormatInt(id, 10)
		} else {
			scope = "group:" + strconv.FormatInt(id, 10)
		}
	}
	return target{ChatID: id, ScopeID: scope}, nil
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
