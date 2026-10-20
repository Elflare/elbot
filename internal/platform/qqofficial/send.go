package qqofficial

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
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

func (a *Adapter) sendContextOutput(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	t, err := a.contextTarget(ctx)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		return delivery.Receipt{}, logResult, err
	}
	return a.sendOutputs(ctx, t, outputs)
}

func (a *Adapter) sendOutputs(ctx context.Context, t sendTarget, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	var receipt delivery.Receipt
	for i, out := range outputs {
		sent, logResult, err := a.sendOutput(ctx, t, out)
		var mediaIndexes []int
		if out.Kind == delivery.KindImage || out.Kind == delivery.KindFile {
			mediaIndexes = []int{i}
		}
		sent = qqOfficialReceipt(sent, t, mediaIndexes...)
		receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
		receipt.SentMessages = append(receipt.SentMessages, sent.SentMessages...)
		if err != nil {
			return receipt, logResult, err
		}
	}
	return receipt, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func qqOfficialReceipt(receipt delivery.Receipt, target sendTarget, mediaIndexes ...int) delivery.Receipt {
	for _, id := range receipt.PlatformMessageIDs {
		if id = strings.TrimSpace(id); id != "" {
			receipt.SentMessages = append(receipt.SentMessages, delivery.SentMessage{PlatformMessageID: id, Platform: platformName, ScopeID: qqOfficialTargetScope(target), OutputIndexes: append([]int(nil), mediaIndexes...)})
		}
	}
	return receipt
}

func qqOfficialTargetScope(target sendTarget) string {
	if target.Kind == targetGroup {
		return "group:" + target.OpenID
	}
	if target.Kind == targetC2C {
		return "c2c:" + target.OpenID
	}
	return ""
}
func (a *Adapter) sendOutput(ctx context.Context, t sendTarget, out delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	switch out.Kind {
	case delivery.KindText:
		return a.sendText(ctx, t, out.Text)
	case delivery.KindReply:
		text := delivery.FallbackText(out)
		return a.sendText(ctx, t, text)
	case delivery.KindEmoticon:
		return a.sendText(ctx, t, delivery.FallbackText(out))
	case delivery.KindImage:
		return a.sendMedia(ctx, t, out, fileTypeImage)
	case delivery.KindFile:
		return a.sendMedia(ctx, t, out, fileTypeFile)
	default:
		return a.sendText(ctx, t, delivery.FallbackText(out))
	}
}

func (a *Adapter) contextTarget(ctx context.Context) (sendTarget, error) {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.Platform != platformName {
		return sendTarget{}, fmt.Errorf("qqofficial send target missing")
	}
	out := delivery.Target{ScopeID: info.Source.ScopeID}
	if id := strings.TrimSpace(info.Source.ConversationID); id != "" {
		switch info.Source.ConversationKind {
		case contextinfo.ConversationGroup:
			out.GroupID = id
		case contextinfo.ConversationPrivate:
			out.PrivateUserID = id
		}
	}
	targets, err := a.targets(out)
	if err != nil {
		return sendTarget{}, err
	}
	t := targets[0]
	t.MsgID = info.PlatformMessageID
	// Ordinary messages reply with msg_id. Event-only sources use event_id.
	if data, ok := info.PlatformData.(messageData); ok && t.MsgID == "" {
		t.EventID = data.EventID
	}
	return t, nil
}

func (a *Adapter) sendText(ctx context.Context, target sendTarget, text string) (delivery.Receipt, globalevents.LogResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}
	if target.Proactive && !a.cfg.allowProactive() {
		err := fmt.Errorf("qqofficial proactive messages are disabled")
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	msg := a.baseMessage(target)
	if a.cfg.markdownByDefault() {
		msg.MsgType = msgTypeMarkdown
		msg.Markdown = &messageMarkdown{Content: text}
		if a.cfg.enableKeyboard() && shouldAttachRiskKeyboard(text) {
			msg.Keyboard = riskKeyboard(a.cfg.AppID)
		}
	} else {
		msg.MsgType = msgTypeText
		msg.Content = text
	}
	resp, err := a.client.sendMessage(ctx, target, msg)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	a.recordMessageAlias(ctx, qqOfficialTargetScope(target), resp.ExtInfo.RefIdx, resp.ID)
	return receiptWithMessageID(resp.ID), globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (a *Adapter) sendMedia(ctx context.Context, target sendTarget, out delivery.Output, fileType int) (delivery.Receipt, globalevents.LogResult, error) {
	if target.Proactive && !a.cfg.allowProactive() {
		{
			err := fmt.Errorf("qqofficial proactive messages are disabled")
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			return delivery.Receipt{}, logResult, err
		}
	}
	source, err := prepareSource(out.Source.URL, out.Source.Path, out.Source.Data)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	uploaded, err := a.client.uploadFile(ctx, target, fileType, source)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	msg := a.baseMessage(target)
	msg.MsgType = msgTypeMedia
	msg.Media = &messageMedia{FileInfo: uploaded.FileInfo}
	resp, err := a.client.sendMessage(ctx, target, msg)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	a.recordMessageAlias(ctx, qqOfficialTargetScope(target), resp.ExtInfo.RefIdx, resp.ID)
	return receiptWithMessageID(resp.ID), globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
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

func (a *Adapter) sendArk(ctx context.Context, target sendTarget, ark messageArk) (delivery.Receipt, globalevents.LogResult, error) {
	if !a.cfg.EnableArk {
		{
			err := fmt.Errorf("qqofficial ark is disabled")
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			return delivery.Receipt{}, logResult, err
		}
	}
	msg := a.baseMessage(target)
	msg.MsgType = msgTypeArk
	msg.Ark = &ark
	resp, err := a.client.sendMessage(ctx, target, msg)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	a.recordMessageAlias(ctx, qqOfficialTargetScope(target), resp.ExtInfo.RefIdx, resp.ID)
	return receiptWithMessageID(resp.ID), globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
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
