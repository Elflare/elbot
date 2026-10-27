// send.go 负责消息编码、媒体上传、实际发送及回执整理。

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

func (a *Adapter) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	ctx, finish, err := a.beginSend(ctx)
	if err != nil {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	defer finish()
	return a.sendContextOutput(ctx, outputs)
}

func (a *Adapter) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	if target.Empty() {
		return a.SendChat(ctx, outputs)
	}

	ctx, finish, err := a.beginSend(ctx)
	if err != nil {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	defer finish()
	targets, err := a.targets(target)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if err == context.Canceled {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	var receipt delivery.Receipt
	for _, target := range targets {
		target.Proactive = true
		sent, logResult, err := a.sendOutputs(ctx, target, outputs)
		receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
		receipt.SentMessages = append(receipt.SentMessages, sent.SentMessages...)
		if err != nil {
			return receipt, logResult, err
		}
	}
	return receipt, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (a *Adapter) targets(target delivery.Target) ([]sendTarget, error) {
	if platformName := strings.TrimSpace(target.Platform); platformName != "" && platformName != a.Name() {
		return nil, fmt.Errorf("qqofficial cannot send to platform %q", platformName)
	}
	if target.Superadmins {
		targets := make([]sendTarget, 0, len(a.cfg.Superadmins))
		for _, id := range a.cfg.Superadmins {
			id = strings.TrimSpace(strings.TrimPrefix(id, platformName+":"))
			if id != "" {
				targets = append(targets, sendTarget{Kind: targetC2C, OpenID: id})
			}
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("qqofficial superadmins are not configured")
		}
		return targets, nil
	}
	if id := strings.TrimSpace(target.PrivateUserID); id != "" {
		return []sendTarget{{Kind: targetC2C, OpenID: id}}, nil
	}
	if id := strings.TrimSpace(target.GroupID); id != "" {
		return []sendTarget{{Kind: targetGroup, OpenID: id}}, nil
	}
	scope := strings.TrimSpace(target.ScopeID)
	if strings.HasPrefix(scope, "c2c:") {
		return []sendTarget{{Kind: targetC2C, OpenID: strings.TrimPrefix(scope, "c2c:")}}, nil
	}
	if strings.HasPrefix(scope, "group:") {
		return []sendTarget{{Kind: targetGroup, OpenID: strings.TrimPrefix(scope, "group:")}}, nil
	}
	return nil, fmt.Errorf("qqofficial target missing private_user_id, group_id or scope_id")
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
	Kind      sendTargetKind // 私聊或群聊目标。
	OpenID    string         // 对应用户或群的开放标识。
	MsgID     string         // 被回复的平台消息 ID，用于被动发送及序号分配。
	EventID   string         // 无消息 ID 时使用的事件回复标识。
	Proactive bool           // 是否属于显式目标的主动发送。
}

type sendTargetKind string

const (
	targetC2C   sendTargetKind = "c2c"
	targetGroup sendTargetKind = "group"
)
