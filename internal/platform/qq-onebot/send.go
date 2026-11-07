// send.go 负责消息发送、输出编码、临时发送连接及实际发送回执。
package qqonebot

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	platformshare "elbot/internal/platform/share"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *Adapter) SendCommandResult(ctx context.Context, _ string, result *command.Result, failure *command.Error) (delivery.Receipt, error) {
	var outputs []delivery.Output
	if result != nil {
		outputs = append(outputs, delivery.Text(platformshare.FormatCommandResult(result)))
	}
	if failure != nil {
		outputs = append(outputs, delivery.Text(failure.Message))
	}
	return a.SendTo(ctx, delivery.Target{}, outputs)
}

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
		end := min(start+qqTextPageRunes, len(runes))
		pages = append(pages, string(runes[start:end]))
	}
	return pages
}

func (a *Adapter) sendTemporary(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	transport := newTransport(a.Name(), a.cfg.URL, a.cfg.AccessToken, time.Duration(a.cfg.APITimeoutSeconds)*time.Second)
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := a.registerTemporary(transport, cancel); err != nil {
		return delivery.Receipt{}, err
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
		return delivery.Receipt{}, err
	}
	cancel()
	readDone = make(chan struct{})
	go func() {
		defer close(readDone)
		transport.readResponses(context.WithoutCancel(ctx))
	}()
	t, err := targetToQQ(target)
	if target.Empty() {
		t, err = a.contextTarget(ctx)
	}
	if err != nil {
		return delivery.Receipt{}, err
	}
	if text, ok := textOutputs(outputs); ok {
		return sendText(ctx, transport, t, text)
	}
	segments, err := outputSegments(a.cfg.SendFileMode, outputs...)
	if err != nil {
		return delivery.Receipt{}, err
	}
	receipt, err := sendSegments(ctx, transport, t, segments)
	return oneBotReceipt(a.Name(), receipt, t, outputs), err
}

func (a *Adapter) SendTo(ctx context.Context, outTarget delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	if outTarget.Superadmins {
		return a.superadmins.SendTo(ctx, outputs, a.SendTo)
	}
	if delivery.UseTemporaryConnection(ctx) {
		return a.sendTemporary(ctx, outTarget, outputs)
	}
	text, ok := textOutputs(outputs)
	if outTarget.Empty() && ok && strings.TrimSpace(text) == "" {
		return delivery.Receipt{}, nil
	}
	var t target
	var err error
	if outTarget.Empty() {
		t, err = a.contextTarget(ctx)
	} else {
		t, err = targetToQQ(outTarget)
	}
	if err != nil {
		return delivery.Receipt{}, err
	}
	if ok {
		return sendText(ctx, a.transport, t, text)
	}
	segments, err := outputSegments(a.cfg.SendFileMode, outputs...)
	if err != nil {
		return delivery.Receipt{}, err
	}
	receipt, err := sendSegments(ctx, a.transport, t, segments)
	return oneBotReceipt(a.Name(), receipt, t, outputs), err
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

func sendText(ctx context.Context, transport *Transport, t target, text string) (delivery.Receipt, error) {
	if strings.TrimSpace(text) == "" {
		return delivery.Receipt{}, nil
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
	return oneBotReceipt(transport.platformName, receiptWithMessageID(id), t, nil), err
}

func sendSegments(ctx context.Context, transport *Transport, t target, segments []Segment) (delivery.Receipt, error) {
	var id string
	var err error
	switch t.MessageType {
	case "private":
		id, err = transport.SendPrivateSegments(ctx, t.UserID, segments)
	case "group":
		id, err = transport.SendGroupSegments(ctx, t.GroupID, segments)
	default:
		err = fmt.Errorf("unsupported message target %q", t.MessageType)
	}
	return receiptWithMessageID(id), err
}

func receiptWithMessageID(id string) delivery.Receipt {
	id = strings.TrimSpace(id)
	if id == "" {
		return delivery.Receipt{}
	}
	return delivery.Receipt{PlatformMessageIDs: []string{id}}
}

func oneBotReceipt(platformName string, receipt delivery.Receipt, target target, outputs []delivery.Output) delivery.Receipt {
	indexes := make([]int, 0, len(outputs))
	for i, out := range outputs {
		if out.Kind == delivery.KindImage || out.Kind == delivery.KindFile || out.Kind == delivery.KindRecord {
			indexes = append(indexes, i)
		}
	}
	for _, id := range receipt.PlatformMessageIDs {
		receipt.SentMessages = append(receipt.SentMessages, delivery.SentMessage{PlatformMessageID: id, Platform: platformName, ChatType: contextinfo.ChatType(target.MessageType), ChatID: oneBotTargetScope(target), OutputIndexes: append([]int(nil), indexes...)})
	}
	return receipt
}

func oneBotTargetScope(target target) string {
	if target.MessageType == "group" {
		return strconv.FormatInt(target.GroupID, 10)
	}
	if target.MessageType == "private" {
		return strconv.FormatInt(target.UserID, 10)
	}
	return ""
}

func (a *Adapter) contextTarget(ctx context.Context) (target, error) {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.Platform != a.Name() {
		return target{}, fmt.Errorf("qq send target missing")
	}
	return targetToQQ(delivery.Target{ChatType: info.Source.ChatType, ChatID: info.Source.ChatID})
}

func targetToQQ(outTarget delivery.Target) (target, error) {
	id, err := strconv.ParseInt(outTarget.ChatID, 10, 64)
	if err != nil {
		return target{}, fmt.Errorf("parse qqonebot conversation id: %w", err)
	}
	switch outTarget.ChatType {
	case contextinfo.ChatPrivate:
		return target{MessageType: "private", UserID: id}, nil
	case contextinfo.ChatGroup:
		return target{MessageType: "group", GroupID: id}, nil
	default:
		return target{}, fmt.Errorf("qqonebot invalid conversation kind %q", outTarget.ChatType)
	}
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
		case delivery.KindImage, delivery.KindFile, delivery.KindRecord:
			file, err := oneBotSourceFile(out.Source, string(out.Kind), sendFileMode)
			if err != nil {
				return nil, err
			}
			data := map[string]any{"file": file}
			if out.Kind == delivery.KindFile {
				if name := strings.TrimSpace(out.Name); name != "" {
					data["name"] = name
				}
			}
			segments = append(segments, Segment{Type: string(out.Kind), Data: data})
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
