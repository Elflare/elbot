package qqonebot

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"elbot/internal/output"
	"elbot/internal/platform"
	"elbot/internal/storage"
)

const qqTextPageRunes = 3000

type Config struct {
	Enabled                  bool     `toml:"enabled"`
	URL                      string   `toml:"ws_url"`
	AccessToken              string   `toml:"access_token"`
	ReconnectIntervalSeconds int      `toml:"reconnect_interval_seconds"`
	APITimeoutSeconds        int      `toml:"api_timeout_seconds"`
	TriggerKeywords          []string `toml:"trigger_keywords"`
	Superadmins              []string `toml:"-"`
	CommandPrefixes          []string `toml:"-"`
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
		pages = append(pages, fmt.Sprintf("%s……（%d/%d）", string(runes[start:end]), len(pages)+1, total))
	}
	return pages
}

func (a *Adapter) sendQQText(ctx context.Context, t target, text string) (string, error) {
	switch t.MessageType {
	case "private":
		return a.transport.SendPrivateMessage(ctx, t.UserID, text)
	case "group":
		return a.transport.SendGroupMessage(ctx, t.GroupID, text)
	default:
		return "", fmt.Errorf("unsupported message target %q", t.MessageType)
	}
}

type Adapter struct {
	cfg         Config
	store       storage.Store
	chatHistory storage.ChatHistoryRepository
	transport   *Transport
	logger      *slog.Logger
	notify      func(context.Context, string)
}

type target struct {
	MessageType string
	UserID      int64
	GroupID     int64
}

type targetKey struct{}

func NewFromPlatformConfig(raw map[string]any, store storage.Store, chatHistory storage.ChatHistoryRepository, logger *slog.Logger, superadmins []string, commandPrefixes []string) (*Adapter, error) {
	var cfg Config
	if err := platform.DecodeConfig(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode qqonebot config: %w", err)
	}
	cfg.Superadmins = superadmins
	cfg.CommandPrefixes = append([]string(nil), commandPrefixes...)
	applyDefaults(&cfg)
	return New(cfg, store, chatHistory, logger), nil
}

func applyDefaults(cfg *Config) {
	if cfg.URL == "" {
		cfg.URL = "ws://127.0.0.1:6700/"
	}
	if cfg.ReconnectIntervalSeconds <= 0 {
		cfg.ReconnectIntervalSeconds = 3
	}
	if cfg.APITimeoutSeconds <= 0 {
		cfg.APITimeoutSeconds = 15
	}
	if len(cfg.CommandPrefixes) == 0 {
		cfg.CommandPrefixes = []string{"/"}
	}
}

func New(cfg Config, store storage.Store, chatHistory storage.ChatHistoryRepository, logger *slog.Logger) *Adapter {
	applyDefaults(&cfg)

	timeout := time.Duration(cfg.APITimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Adapter{
		cfg:         cfg,
		store:       store,
		chatHistory: chatHistory,
		transport: &Transport{
			URL:         cfg.URL,
			AccessToken: cfg.AccessToken,
			Timeout:     timeout,
		},
		logger: logger,
	}
}

func (a *Adapter) Name() string { return "qqonebot" }

func (a *Adapter) Enabled() bool { return a.cfg.Enabled }

func (a *Adapter) SetConnectNotifier(notify func(context.Context, string)) {
	a.notify = notify
}

func (a *Adapter) notifyConnected(ctx context.Context) {
	if a.notify != nil {
		a.notify(ctx, a.Name())
	}
}

func (a *Adapter) Run(ctx context.Context, handler platform.PlatformHandler) error {
	if !a.cfg.Enabled {
		return nil
	}
	interval := time.Duration(a.cfg.ReconnectIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 3 * time.Second
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.transport.Connect(ctx); err != nil {
			a.logWarn("onebot connect failed", "error", err)
			if !sleepContext(ctx, interval) {
				return ctx.Err()
			}
			continue
		}
		a.logInfo("onebot connected", "url", a.cfg.URL)
		go a.notifyConnected(ctx)
		err := a.readLoop(ctx, handler)
		a.transport.Close(websocket.StatusNormalClosure, "reconnect")
		if err != nil && !errors.Is(err, context.Canceled) {
			a.logWarn("onebot disconnected", "error", err)
		}
		if !sleepContext(ctx, interval) {
			return ctx.Err()
		}
	}
}

func (a *Adapter) SendChat(ctx context.Context, out output.Output) (platform.Receipt, error) {
	if out.Kind == output.KindText {
		return a.sendContextText(ctx, out.Text)
	}
	return a.sendContextOutput(ctx, out)
}

func (a *Adapter) SendNotice(ctx context.Context, outTarget output.Target, out output.Output) (platform.Receipt, error) {
	if outTarget.Empty() && isGroupToolPreviewNotice(ctx, out) {
		return platform.Receipt{}, nil
	}
	if outTarget.Empty() {
		return a.SendChat(ctx, out)
	}
	return a.sendTarget(ctx, outTarget, out)
}

func isGroupToolPreviewNotice(ctx context.Context, out output.Output) bool {
	if out.Kind != output.KindText || !strings.HasPrefix(strings.TrimSpace(out.Text), "[tool]") {
		return false
	}
	t, ok := ctx.Value(targetKey{}).(target)
	return ok && t.MessageType == "group"
}

func (a *Adapter) sendContextText(ctx context.Context, text string) (platform.Receipt, error) {
	if strings.TrimSpace(text) == "" {
		return platform.Receipt{}, nil
	}
	t, ok := ctx.Value(targetKey{}).(target)
	if !ok {
		return platform.Receipt{}, fmt.Errorf("qq send target missing")
	}
	var receipt platform.Receipt
	for _, page := range qqTextPages(text) {
		id, err := a.sendQQText(ctx, t, page)
		if err != nil {
			return platform.Receipt{}, err
		}
		if strings.TrimSpace(id) != "" {
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, id)
		}
	}
	return receipt, nil
}

func (a *Adapter) sendContextOutput(ctx context.Context, out output.Output) (platform.Receipt, error) {
	t, ok := ctx.Value(targetKey{}).(target)
	if !ok {
		return platform.Receipt{}, fmt.Errorf("qq send target missing")
	}
	segments, err := outputSegments(out)
	if err != nil {
		return platform.Receipt{}, err
	}
	switch t.MessageType {
	case "private":
		id, err := a.transport.SendPrivateSegments(ctx, t.UserID, segments)
		return receiptWithMessageID(id), err
	case "group":
		id, err := a.transport.SendGroupSegments(ctx, t.GroupID, segments)
		return receiptWithMessageID(id), err
	default:
		return platform.Receipt{}, fmt.Errorf("unsupported message target %q", t.MessageType)
	}
}

func (a *Adapter) sendTarget(ctx context.Context, outTarget output.Target, out output.Output) (platform.Receipt, error) {
	if outTarget.Superadmins {
		if len(a.cfg.Superadmins) == 0 {
			return platform.Receipt{}, fmt.Errorf("qqonebot superadmins are not configured")
		}
		var receipt platform.Receipt
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
			sent, err := a.sendTarget(ctx, copyTarget, out)
			if err != nil {
				return platform.Receipt{}, err
			}
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
		}
		return receipt, nil
	}
	t, err := targetToQQ(outTarget)
	if err != nil {
		return platform.Receipt{}, err
	}
	targetCtx := context.WithValue(ctx, targetKey{}, t)
	if out.Kind == output.KindText {
		return a.sendContextText(targetCtx, out.Text)
	}
	return a.sendContextOutput(targetCtx, out)
}

func receiptWithMessageID(id string) platform.Receipt {
	id = strings.TrimSpace(id)
	if id == "" {
		return platform.Receipt{}
	}
	return platform.Receipt{PlatformMessageIDs: []string{id}}
}

func targetToQQ(outTarget output.Target) (target, error) {
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

func outputSegments(out output.Output) ([]Segment, error) {
	switch out.Kind {
	case output.KindReply:
		replyID := strings.TrimSpace(out.ReplyToPlatformMessageID)
		if replyID == "" {
			return nil, fmt.Errorf("reply target message id is empty")
		}
		return []Segment{
			{Type: "reply", Data: map[string]any{"id": replyID}},
			{Type: "text", Data: map[string]any{"text": out.Text}},
		}, nil
	case output.KindEmoticon, output.KindImage:
		file, err := base64SourceFile(out.Source, "image")
		if err != nil {
			return nil, err
		}
		return []Segment{{Type: "image", Data: map[string]any{"file": file}}}, nil
	case output.KindFile:
		file, err := base64SourceFile(out.Source, "file")
		if err != nil {
			return nil, err
		}
		data := map[string]any{"file": file}
		if name := strings.TrimSpace(out.Name); name != "" {
			data["name"] = name
		}
		return []Segment{{Type: "file", Data: data}}, nil
	case output.KindAt:
		qq := strings.TrimSpace(out.Name)
		if qq == "" {
			qq = strings.TrimSpace(out.Text)
		}
		if qq == "" {
			return nil, fmt.Errorf("at target is empty")
		}
		return []Segment{{Type: "at", Data: map[string]any{"qq": qq}}}, nil
	default:
		return nil, fmt.Errorf("unsupported output kind %q", out.Kind)
	}
}

func base64SourceFile(source output.Source, label string) (string, error) {
	if len(source.Data) > 0 {
		return "base64://" + base64.StdEncoding.EncodeToString(source.Data), nil
	}
	if url := strings.TrimSpace(source.URL); url != "" {
		return url, nil
	}
	path := strings.TrimSpace(source.Path)
	if path == "" {
		return "", fmt.Errorf("%s path is empty", label)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s %q: %w", label, path, err)
	}
	return "base64://" + base64.StdEncoding.EncodeToString(data), nil
}

func (a *Adapter) readLoop(ctx context.Context, handler platform.PlatformHandler) error {
	for {
		event, err := a.transport.Read(ctx)
		if err != nil {
			return err
		}
		if event.PostType == "" {
			continue
		}
		if !a.isMessageEvent(event) {
			continue
		}
		go a.handleEvent(ctx, handler, event)
	}
}

func (a *Adapter) handleEvent(ctx context.Context, handler platform.PlatformHandler, event Event) {
	normalized := normalizeMessage(event.Message, event.RawMessage, event.SelfID)
	a.recordChatMessage(ctx, event, normalized)
	if !a.shouldHandle(event, normalized) {
		return
	}
	text := normalized.Text
	if stripped, ok := platform.StripTriggerKeyword(text, a.cfg.TriggerKeywords); ok {
		text = stripped
	}
	if text == "" && !platform.HasCommandPrefix(event.RawMessage, a.cfg.CommandPrefixes) {
		return
	}
	currentSegments := a.resolveImageSegments(ctx, normalized.Segments)
	messageCtx := platform.MessageContext{
		Platform:              a.Name(),
		PlatformUserID:        strconv.FormatInt(event.UserID, 10),
		DisplayName:           displayName(event.Sender, event.UserID),
		ScopeID:               scopeID(event),
		Sender:                a,
		BufferAssistantOutput: true,
		Segments:              finalMessageSegments(text, currentSegments, nil),
	}
	msgCtx := platform.WithMessageContext(ctx, messageCtx)
	msgCtx = context.WithValue(msgCtx, targetKey{}, target{MessageType: event.MessageType, UserID: event.UserID, GroupID: event.GroupID})

	var referenceSegments []platform.MessageSegment
	if normalized.ReplyID != "" {
		trimmed := strings.TrimSpace(text)
		if platform.HasCommandPrefix(trimmed, a.cfg.CommandPrefixes) {
			text = a.commandWithReference(event, normalized.ReplyID, trimmed)
		} else if a.isLatestOwnAssistantReference(msgCtx, event, normalized.ReplyID) {
			// 用户常会引用机器人最后一条消息来继续说话；这种情况不需要 fork，也不需要重复塞引用内容。
		} else if forkFromMessageID := a.forkableReferenceMessageID(msgCtx, event, normalized.ReplyID); forkFromMessageID != "" {
			messageCtx.ForkFromMessageID = forkFromMessageID
			msgCtx = platform.WithMessageContext(ctx, messageCtx)
			msgCtx = context.WithValue(msgCtx, targetKey{}, target{MessageType: event.MessageType, UserID: event.UserID, GroupID: event.GroupID})
		} else {
			text, referenceSegments = a.withReference(msgCtx, event, normalized.ReplyID, text)
		}
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	messageCtx.Segments = finalMessageSegments(text, currentSegments, referenceSegments)
	msgCtx = platform.WithMessageContext(ctx, messageCtx)
	msgCtx = context.WithValue(msgCtx, targetKey{}, target{MessageType: event.MessageType, UserID: event.UserID, GroupID: event.GroupID})
	if err := handler.HandleMessage(msgCtx, text); err != nil {
		a.logWarn("handle qq message failed", "error", err, "message_id", event.MessageID)
	}
}

func (a *Adapter) resolveImageSegments(ctx context.Context, segments []platform.MessageSegment) []platform.MessageSegment {
	if len(segments) == 0 || a.transport == nil {
		return segments
	}
	out := append([]platform.MessageSegment(nil), segments...)
	for i := range out {
		if out[i].Type != platform.SegmentImage || out[i].URL != "" || out[i].Name == "" {
			continue
		}
		data, err := a.transport.GetImage(ctx, out[i].Name)
		if err != nil {
			a.logWarn("get qq image failed", "file", out[i].Name, "error", err)
			continue
		}
		url, err := imageFileDataURL(data.File)
		if err != nil {
			a.logWarn("read qq image failed", "file", data.File, "error", err)
			continue
		}
		out[i].URL = url
	}
	return out
}

func imageFileDataURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func (a *Adapter) shouldHandle(event Event, normalized NormalizedMessage) bool {
	if event.MessageType == "private" {
		return true
	}
	if event.MessageType != "group" {
		return false
	}
	text := strings.TrimSpace(normalized.Text)
	if platform.HasCommandPrefix(text, a.cfg.CommandPrefixes) {
		return true
	}
	if _, ok := platform.StripTriggerKeyword(text, a.cfg.TriggerKeywords); ok {
		return true
	}
	if normalized.AtSelf {
		return true
	}
	return normalized.ReplyID != "" && a.isBotReply(event, normalized.ReplyID)
}

func (a *Adapter) isBotReply(event Event, replyID string) bool {
	if a.store != nil {
		msg, err := a.store.Messages().FindByPlatformMessage(context.Background(), a.Name(), scopeID(event), replyID)
		if err == nil && msg.Role == storage.RoleAssistant {
			return true
		}
	}
	if a.transport == nil {
		return false
	}
	data, err := a.transport.GetMessage(context.Background(), replyID)
	return err == nil && data.UserID == event.SelfID
}

func (a *Adapter) commandWithReference(event Event, replyID, text string) string {
	name, ok := platform.CommandName(text, a.cfg.CommandPrefixes)
	if !ok || name != "fork" || a.store == nil {
		return text
	}
	msg, err := a.store.Messages().FindByPlatformMessage(context.Background(), a.Name(), scopeID(event), replyID)
	if err != nil || msg.Role != storage.RoleAssistant {
		return text
	}
	return "/fork " + msg.ID
}

func (a *Adapter) forkableReferenceMessageID(ctx context.Context, event Event, replyID string) string {
	msg, ok := a.ownReferenceAssistant(ctx, event, replyID)
	if !ok || a.isLatestAssistantMessage(ctx, msg) {
		return ""
	}
	return msg.ID
}

func (a *Adapter) isLatestOwnAssistantReference(ctx context.Context, event Event, replyID string) bool {
	msg, ok := a.ownReferenceAssistant(ctx, event, replyID)
	return ok && a.isLatestAssistantMessage(ctx, msg)
}

func (a *Adapter) ownReferenceAssistant(ctx context.Context, event Event, replyID string) (*storage.Message, bool) {
	msg, err := a.referenceAssistantMessage(ctx, event, replyID)
	if err != nil {
		return nil, false
	}
	session, err := a.store.Sessions().Get(ctx, msg.SessionID)
	if err != nil {
		return nil, false
	}
	actorID := a.Name() + ":" + strconv.FormatInt(event.UserID, 10)
	if session.OwnerID != actorID || session.Platform != a.Name() || session.PlatformScopeID != scopeID(event) {
		return nil, false
	}
	return msg, true
}

func (a *Adapter) referenceAssistantMessage(ctx context.Context, event Event, replyID string) (*storage.Message, error) {
	if a.store == nil {
		return nil, storage.ErrNotFound
	}
	msg, err := a.store.Messages().FindByPlatformMessage(ctx, a.Name(), scopeID(event), replyID)
	if err != nil || msg.Role != storage.RoleAssistant {
		return nil, storage.ErrNotFound
	}
	return msg, nil
}

func (a *Adapter) isLatestAssistantMessage(ctx context.Context, msg *storage.Message) bool {
	messages, err := a.store.Messages().ListBySession(ctx, msg.SessionID)
	if err != nil {
		return true
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == storage.RoleAssistant {
			return messages[i].ID == msg.ID
		}
	}
	return true
}

func (a *Adapter) withReference(ctx context.Context, event Event, replyID, text string) (string, []platform.MessageSegment) {
	label := "引用"
	content := ""
	var segments []platform.MessageSegment
	if a.transport != nil {
		if data, err := a.transport.GetMessage(ctx, replyID); err == nil {
			ref := normalizeMessage(data.Message, data.RawMessage, event.SelfID)
			content = ref.Text
			segments = a.resolveImageSegments(ctx, ref.Segments)
			if data.UserID != 0 {
				label = "引用：" + displayName(data.Sender, data.UserID)
			}
		}
	}
	if a.store != nil {
		if msg, err := a.store.Messages().FindByPlatformMessage(ctx, a.Name(), scopeID(event), replyID); err == nil {
			if msg.Role == storage.RoleAssistant && label == "引用" {
				label = "引用：bot"
			}
			if strings.TrimSpace(msg.Content) != "" {
				content = msg.Content
			}
		}
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return text, segments
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Sprintf("[%s]：%s", label, content), segments
	}
	return fmt.Sprintf("[%s]：%s\n\n%s", label, content, text), segments
}

func (a *Adapter) recordChatMessage(ctx context.Context, event Event, normalized NormalizedMessage) {
	if a.chatHistory == nil || strings.TrimSpace(normalized.Text) == "" || event.MessageID == 0 {
		return
	}
	createdAt := storage.Now()
	if event.Time > 0 {
		createdAt = time.Unix(event.Time, 0)
	}
	message := &storage.ChatMessage{
		Platform:                 a.Name(),
		PlatformScopeID:          scopeID(event),
		ScopeType:                event.MessageType,
		PlatformMessageID:        strconv.FormatInt(event.MessageID, 10),
		SenderID:                 strconv.FormatInt(event.UserID, 10),
		SenderName:               displayName(event.Sender, event.UserID),
		Text:                     normalized.Text,
		Raw:                      event.RawMessage,
		ReplyToPlatformMessageID: normalized.ReplyID,
		CreatedAt:                createdAt,
	}
	if err := a.chatHistory.Append(ctx, message); err != nil {
		a.logWarn("record qq chat message failed", "error", err, "message_id", event.MessageID)
	}
}

func finalMessageSegments(text string, current, referenced []platform.MessageSegment) []platform.MessageSegment {
	out := make([]platform.MessageSegment, 0, 1+len(current)+len(referenced))
	if strings.TrimSpace(text) != "" {
		out = append(out, platform.MessageSegment{Type: platform.SegmentText, Text: text})
	}
	out = appendNonTextSegments(out, current)
	out = appendNonTextSegments(out, referenced)
	return out
}

func appendNonTextSegments(out []platform.MessageSegment, segments []platform.MessageSegment) []platform.MessageSegment {
	for _, segment := range segments {
		if segment.Type != platform.SegmentText {
			out = append(out, segment)
		}
	}
	return out
}

func (a *Adapter) isMessageEvent(event Event) bool {
	return event.PostType == "message" && (event.MessageType == "private" || event.MessageType == "group")
}

func scopeID(event Event) string {
	if event.MessageType == "group" {
		return fmt.Sprintf("group:%d", event.GroupID)
	}
	return fmt.Sprintf("private:%d", event.UserID)
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (a *Adapter) logInfo(msg string, args ...any) {
	if a.logger != nil {
		a.logger.Info(msg, args...)
	}
}

func (a *Adapter) logWarn(msg string, args ...any) {
	if a.logger != nil {
		a.logger.Warn(msg, args...)
	}
}
