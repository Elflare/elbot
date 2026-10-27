// send.go 负责消息发送、输出编码、临时发送连接及实际发送回执。
package qqonebot

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	qqTextPageRunes     = 3000
	sendFileModeBase64  = "base64"
	sendFileModeFileURI = "file_uri"
)

type target struct {
	MessageType string
	UserID      int64
	GroupID     int64
}

func qqTextPages(text string) []string {
	runes := []rune(text)
	if len(runes) <= qqTextPageRunes {
		return []string{text}
	}
	total := (len(runes) + qqTextPageRunes - 1) / qqTextPageRunes
	pages := make([]string, 0, total)
	for start := 0; start < len(runes); start += qqTextPageRunes {
		end := start + qqTextPageRunes
		if end > len(runes) {
			end = len(runes)
		}
		pages = append(pages, string(runes[start:end]))
	}
	return pages
}

func (a *Adapter) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	if text, ok := textOutputs(outputs); ok {
		return a.sendContextText(ctx, text)
	}
	t, err := contextTarget(ctx)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		return delivery.Receipt{}, logResult, err
	}
	segments, err := outputSegments(a.cfg.SendFileMode, outputs...)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	receipt, logResult, err := a.sendSegments(ctx, t, segments)
	return oneBotReceipt(receipt, t, outputs), logResult, err
}

func (a *Adapter) sendTemporary(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	transport := newTransport(a.cfg.URL, a.cfg.AccessToken, time.Duration(a.cfg.APITimeoutSeconds)*time.Second)
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := a.registerTemporary(transport, cancel); err != nil {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	var readDone chan struct{}
	defer func() {
		_ = transport.Close(context.Background())
		if readDone != nil {
			<-readDone
		}
		a.unregisterTemporary(transport)
	}()
	if err := transport.Connect(dialCtx); err != nil {
		{
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return delivery.Receipt{}, logResult, err
		}
	}
	cancel()
	readDone = make(chan struct{})
	go func() {
		defer close(readDone)
		transport.readResponses(context.WithoutCancel(ctx))
	}()
	t, err := targetToQQ(target)
	if target.Empty() {
		t, err = contextTarget(ctx)
	}
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	if text, ok := textOutputs(outputs); ok {
		return sendText(ctx, transport, t, text)
	}
	segments, err := outputSegments(a.cfg.SendFileMode, outputs...)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	var id string
	switch t.MessageType {
	case "private":
		id, err = transport.SendPrivateSegments(ctx, t.UserID, segments)
	case "group":
		id, err = transport.SendGroupSegments(ctx, t.GroupID, segments)
	default:
		err = fmt.Errorf("unsupported message target %q", t.MessageType)
	}
	receipt := receiptWithMessageID(id)
	{
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
		if err != nil {
			logResult.ResultStatus = globalevents.ResultFailed
			logResult.Content = err.Error()
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
		}
		return oneBotReceipt(receipt, t, outputs), logResult, err
	}
}

func (a *Adapter) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	outTarget := target
	if outTarget.Superadmins {
		if len(a.cfg.Superadmins) == 0 {
			{
				err := fmt.Errorf("qqonebot superadmins are not configured")
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
			sent, logResult, err := a.SendTo(ctx, copyTarget, outputs)
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
			receipt.SentMessages = append(receipt.SentMessages, sent.SentMessages...)
			if err != nil {
				return receipt, logResult, err
			}
		}
		return receipt, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}
	if delivery.UseTemporaryConnection(ctx) {
		return a.sendTemporary(ctx, target, outputs)
	}
	if outTarget.Empty() {
		return a.SendChat(ctx, outputs)
	}
	t, err := targetToQQ(outTarget)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	if text, ok := textOutputs(outputs); ok {
		return sendText(ctx, a.transport, t, text)
	}
	segments, err := outputSegments(a.cfg.SendFileMode, outputs...)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	receipt, logResult, err := a.sendSegments(ctx, t, segments)
	return oneBotReceipt(receipt, t, outputs), logResult, err
}

func textOutputs(outputs []delivery.Output) (string, bool) {
	var text strings.Builder
	for _, out := range outputs {
		if out.Kind != delivery.KindText {
			return "", false
		}
		text.WriteString(out.Text)
	}
	return text.String(), true
}

func (a *Adapter) sendContextText(ctx context.Context, text string) (delivery.Receipt, globalevents.LogResult, error) {
	if strings.TrimSpace(text) == "" {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}
	t, err := contextTarget(ctx)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		return delivery.Receipt{}, logResult, err
	}
	return sendText(ctx, a.transport, t, text)
}

func sendText(ctx context.Context, transport *Transport, t target, text string) (delivery.Receipt, globalevents.LogResult, error) {
	if strings.TrimSpace(text) == "" {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}
	pages := qqTextPages(text)
	var nodes []Segment
	if len(pages) > 1 {
		for _, page := range pages {
			nodes = append(nodes, Segment{Type: "node", Data: map[string]any{
				"content": []Segment{{Type: "text", Data: map[string]any{"text": page}}},
			}})
		}
	}
	var id string
	var err error
	switch t.MessageType {
	case "private":
		if len(nodes) > 0 {
			id, err = transport.SendPrivateForwardMessage(ctx, t.UserID, nodes)
		} else {
			id, err = transport.SendPrivateMessage(ctx, t.UserID, text)
		}
	case "group":
		if len(nodes) > 0 {
			id, err = transport.SendGroupForwardMessage(ctx, t.GroupID, nodes)
		} else {
			id, err = transport.SendGroupMessage(ctx, t.GroupID, text)
		}
	default:
		err = fmt.Errorf("unsupported message target %q", t.MessageType)
	}
	{
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
		if err != nil {
			logResult.ResultStatus = globalevents.ResultFailed
			logResult.Content = err.Error()
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
		}
		return oneBotReceipt(receiptWithMessageID(id), t, nil), logResult, err
	}
}

func (a *Adapter) sendSegments(ctx context.Context, t target, segments []Segment) (delivery.Receipt, globalevents.LogResult, error) {
	switch t.MessageType {
	case "private":
		id, err := a.transport.SendPrivateSegments(ctx, t.UserID, segments)
		{
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
			if err != nil {
				logResult.ResultStatus = globalevents.ResultFailed
				logResult.Content = err.Error()
				if errors.Is(err, context.Canceled) {
					logResult.ResultStatus = globalevents.ResultCanceled
				}
			}
			return receiptWithMessageID(id), logResult, err
		}
	case "group":
		id, err := a.transport.SendGroupSegments(ctx, t.GroupID, segments)
		{
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
			if err != nil {
				logResult.ResultStatus = globalevents.ResultFailed
				logResult.Content = err.Error()
				if errors.Is(err, context.Canceled) {
					logResult.ResultStatus = globalevents.ResultCanceled
				}
			}
			return receiptWithMessageID(id), logResult, err
		}
	default:
		{
			err := fmt.Errorf("unsupported message target %q", t.MessageType)
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			return delivery.Receipt{}, logResult, err
		}
	}
}

func receiptWithMessageID(id string) delivery.Receipt {
	id = strings.TrimSpace(id)
	if id == "" {
		return delivery.Receipt{}
	}
	return delivery.Receipt{PlatformMessageIDs: []string{id}}
}

func oneBotReceipt(receipt delivery.Receipt, target target, outputs []delivery.Output) delivery.Receipt {
	indexes := make([]int, 0, len(outputs))
	for i, out := range outputs {
		if out.Kind == delivery.KindImage || out.Kind == delivery.KindFile || out.Kind == delivery.KindRecord {
			indexes = append(indexes, i)
		}
	}
	for _, id := range receipt.PlatformMessageIDs {
		if id = strings.TrimSpace(id); id != "" {
			receipt.SentMessages = append(receipt.SentMessages, delivery.SentMessage{PlatformMessageID: id, Platform: "qqonebot", ScopeID: oneBotTargetScope(target), OutputIndexes: append([]int(nil), indexes...)})
		}
	}
	return receipt
}

func oneBotTargetScope(target target) string {
	if target.MessageType == "group" {
		return "group:" + strconv.FormatInt(target.GroupID, 10)
	}
	if target.MessageType == "private" {
		return "private:" + strconv.FormatInt(target.UserID, 10)
	}
	return ""
}

func contextTarget(ctx context.Context) (target, error) {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.Platform != "qqonebot" {
		return target{}, fmt.Errorf("qq send target missing")
	}
	out := delivery.Target{ScopeID: info.Source.ScopeID}
	if info.Source.ConversationID != "" {
		switch info.Source.ConversationKind {
		case contextinfo.ConversationGroup:
			out.GroupID = info.Source.ConversationID
		case contextinfo.ConversationPrivate:
			out.PrivateUserID = info.Source.ConversationID
		}
	}
	return targetToQQ(out)
}

func targetToQQ(outTarget delivery.Target) (target, error) {
	if strings.TrimSpace(outTarget.PrivateUserID) == "" && strings.TrimSpace(outTarget.GroupID) == "" {
		scope := strings.TrimSpace(outTarget.ScopeID)
		if strings.HasPrefix(scope, "private:") {
			outTarget.PrivateUserID = strings.TrimPrefix(scope, "private:")
		} else if strings.HasPrefix(scope, "group:") {
			outTarget.GroupID = strings.TrimPrefix(scope, "group:")
		}
	}
	if userID := strings.TrimSpace(outTarget.PrivateUserID); userID != "" {
		id, err := strconv.ParseInt(userID, 10, 64)
		if err != nil {
			return target{}, fmt.Errorf("parse qqonebot private user id: %w", err)
		}
		return target{MessageType: "private", UserID: id}, nil
	}
	if groupID := strings.TrimSpace(outTarget.GroupID); groupID != "" {
		id, err := strconv.ParseInt(groupID, 10, 64)
		if err != nil {
			return target{}, fmt.Errorf("parse qqonebot group id: %w", err)
		}
		return target{MessageType: "group", GroupID: id}, nil
	}
	return target{}, fmt.Errorf("qqonebot target missing private_user_id, group_id or scope_id")
}

func outputSegments(sendFileMode string, outputs ...delivery.Output) ([]Segment, error) {
	segments := make([]Segment, 0, len(outputs))
	for _, out := range outputs {
		switch out.Kind {
		case delivery.KindText:
			segments = append(segments, Segment{Type: "text", Data: map[string]any{"text": out.Text}})
		case delivery.KindReply:
			replyID := strings.TrimSpace(out.ReplyToPlatformMessageID)
			segments = append(segments, Segment{Type: "reply", Data: map[string]any{"id": replyID}}, Segment{Type: "text", Data: map[string]any{"text": out.Text}})
		case delivery.KindEmoticon:
			id := strings.TrimSpace(out.EmoticonID)
			segments = append(segments, Segment{Type: "face", Data: map[string]any{"id": id}})
		case delivery.KindImage:
			file, err := oneBotSourceFile(out.Source, "image", sendFileMode)
			if err != nil {
				return nil, err
			}
			segments = append(segments, Segment{Type: "image", Data: map[string]any{"file": file}})
		case delivery.KindFile:
			file, err := oneBotSourceFile(out.Source, "file", sendFileMode)
			if err != nil {
				return nil, err
			}
			data := map[string]any{"file": file}
			if name := strings.TrimSpace(out.Name); name != "" {
				data["name"] = name
			}
			segments = append(segments, Segment{Type: "file", Data: data})
		case delivery.KindRecord:
			file, err := oneBotSourceFile(out.Source, "record", sendFileMode)
			if err != nil {
				return nil, err
			}
			segments = append(segments, Segment{Type: "record", Data: map[string]any{"file": file}})
		case delivery.KindAt:
			qq := strings.TrimSpace(out.Name)
			segments = append(segments, Segment{Type: "at", Data: map[string]any{"qq": qq}})
		default:
			return nil, fmt.Errorf("unsupported output kind %q", out.Kind)
		}
	}
	return segments, nil
}

func oneBotSourceFile(source delivery.Source, label, sendFileMode string) (string, error) {
	if len(source.Data) > 0 {
		return "base64://" + base64.StdEncoding.EncodeToString(source.Data), nil
	}
	if sourceURL := strings.TrimSpace(source.URL); sourceURL != "" {
		return sourceURL, nil
	}
	path := strings.TrimSpace(source.Path)
	switch sendFileMode {
	case sendFileModeBase64:
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s path %q: %w", label, path, err)
		}
		return "base64://" + base64.StdEncoding.EncodeToString(data), nil
	case sendFileModeFileURI:
		return localPathFileURI(path, label)
	default:
		return "", fmt.Errorf("unsupported qqonebot send_file_mode %q", sendFileMode)
	}
}

func localPathFileURI(path, label string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s path %q: %w", label, path, err)
	}
	slashPath := filepath.ToSlash(abs)
	if strings.HasPrefix(slashPath, "//") {
		trimmed := strings.TrimPrefix(slashPath, "//")
		host, rest, ok := strings.Cut(trimmed, "/")
		if ok {
			return (&url.URL{Scheme: "file", Host: host, Path: "/" + rest}).String(), nil
		}
	}
	if filepath.VolumeName(abs) != "" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String(), nil
}
