package qqofficial

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/platform"
)

const (
	msgTypeText     = 0
	msgTypeMarkdown = 2
	msgTypeArk      = 3
	msgTypeMedia    = 7

	fileTypeImage = 1
	fileTypeFile  = 4
)

func receiptWithMessageID(id string) delivery.Receipt {
	id = strings.TrimSpace(id)
	if id == "" {
		return delivery.Receipt{}
	}
	return delivery.Receipt{PlatformMessageIDs: []string{id}}
}

func (a *Adapter) sendContextOutput(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	t, err := a.contextTarget(ctx)
	if err != nil {
		return delivery.Receipt{}, err
	}
	var receipt delivery.Receipt
	for _, out := range outputs {
		sent, err := a.sendOutput(ctx, t, out)
		if err != nil {
			return delivery.Receipt{}, err
		}
		receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
	}
	return receipt, nil
}

func (a *Adapter) sendOutput(ctx context.Context, t sendTarget, out delivery.Output) (delivery.Receipt, error) {
	switch out.Kind {
	case delivery.KindText:
		return a.sendText(ctx, t, out.Text)
	case delivery.KindReply:
		text := delivery.FallbackText(out)
		return a.sendText(ctx, t, text)
	case delivery.KindImage, delivery.KindEmoticon:
		return a.sendMedia(ctx, t, out, fileTypeImage)
	case delivery.KindFile:
		return a.sendMedia(ctx, t, out, fileTypeFile)
	default:
		return a.sendText(ctx, t, delivery.FallbackText(out))
	}
}

func (a *Adapter) contextTarget(ctx context.Context) (sendTarget, error) {
	if t, ok := ctx.Value(targetKey{}).(sendTarget); ok && strings.TrimSpace(t.OpenID) != "" {
		return t, nil
	}
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		openID := strings.TrimSpace(msg.PlatformUserID)
		if strings.HasPrefix(openID, platformName+":") {
			openID = strings.TrimPrefix(openID, platformName+":")
		}
		if openID != "" {
			return sendTarget{OpenID: openID, MsgID: metaString(msg.Meta, metaMsgID), EventID: metaString(msg.Meta, metaEventID)}, nil
		}
	}
	return sendTarget{}, fmt.Errorf("qqofficial send target missing")
}

func (a *Adapter) sendText(ctx context.Context, target sendTarget, text string) (delivery.Receipt, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return delivery.Receipt{}, nil
	}
	if target.Proactive && !a.cfg.allowProactive() {
		return delivery.Receipt{}, fmt.Errorf("qqofficial proactive messages are disabled")
	}
	if a.cfg.markdownByDefault() {
		msg := a.baseMessage(target)
		msg.MsgType = msgTypeMarkdown
		msg.Markdown = &messageMarkdown{Content: text}
		if a.cfg.enableKeyboard() && shouldAttachRiskKeyboard(text) {
			msg.Keyboard = riskKeyboard(a.cfg.AppID)
		}
		resp, err := a.client.sendMessage(ctx, target.OpenID, msg)
		if err == nil {
			return receiptWithMessageID(resp.ID), nil
		}
		a.logWarn(ctx, "qqofficial markdown send failed, fallback to text", "error", err)
	}
	msg := a.baseMessage(target)
	msg.MsgType = msgTypeText
	msg.Content = text
	resp, err := a.client.sendMessage(ctx, target.OpenID, msg)
	if err != nil {
		return delivery.Receipt{}, err
	}
	return receiptWithMessageID(resp.ID), nil
}

func (a *Adapter) sendMedia(ctx context.Context, target sendTarget, out delivery.Output, fileType int) (delivery.Receipt, error) {
	if target.Proactive && !a.cfg.allowProactive() {
		return delivery.Receipt{}, fmt.Errorf("qqofficial proactive messages are disabled")
	}
	source, err := prepareSource(out.Source.URL, out.Source.Path, out.Source.Data)
	if err != nil {
		return delivery.Receipt{}, err
	}
	uploaded, err := a.client.uploadFile(ctx, target.OpenID, fileType, source)
	if err != nil {
		return delivery.Receipt{}, err
	}
	msg := a.baseMessage(target)
	msg.MsgType = msgTypeMedia
	msg.Media = &messageMedia{FileInfo: uploaded.FileInfo}
	resp, err := a.client.sendMessage(ctx, target.OpenID, msg)
	if err != nil {
		return delivery.Receipt{}, err
	}
	return receiptWithMessageID(resp.ID), nil
}

func (a *Adapter) baseMessage(target sendTarget) messageToCreate {
	msg := messageToCreate{}
	if strings.TrimSpace(target.MsgID) != "" {
		msg.MsgID = strings.TrimSpace(target.MsgID)
		msg.MsgSeq = a.nextMsgSeq(msg.MsgID)
	} else if strings.TrimSpace(target.EventID) != "" {
		msg.EventID = strings.TrimSpace(target.EventID)
	}
	return msg
}

func (a *Adapter) sendArk(ctx context.Context, target sendTarget, ark messageArk) (delivery.Receipt, error) {
	if !a.cfg.EnableArk {
		return delivery.Receipt{}, fmt.Errorf("qqofficial ark is disabled")
	}
	msg := a.baseMessage(target)
	msg.MsgType = msgTypeArk
	msg.Ark = &ark
	resp, err := a.client.sendMessage(ctx, target.OpenID, msg)
	if err != nil {
		return delivery.Receipt{}, err
	}
	return receiptWithMessageID(resp.ID), nil
}

func metaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	switch value := meta[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return ""
	}
}
