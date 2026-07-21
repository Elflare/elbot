package qqonebot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/platform/refcontext"
	"elbot/internal/security"
	"elbot/internal/storage"
)

const (
	qqTextPageRunes     = 3000
	sendFileModeBase64  = "base64"
	sendFileModeFileURI = "file_uri"
)

type Config struct {
	Enabled                  bool     `toml:"enabled"`
	URL                      string   `toml:"ws_url"`
	AccessToken              string   `toml:"access_token"`
	ReconnectIntervalSeconds int      `toml:"reconnect_interval_seconds"`
	APITimeoutSeconds        int      `toml:"api_timeout_seconds"`
	TriggerKeywords          []string `toml:"trigger_keywords"`
	SendFileMode             string   `toml:"send_file_mode"`
	AttachmentDir            string   `toml:"-"`
	MaxReceiveFileBytes      int64    `toml:"-"`
	DownloadTimeoutSecs      int      `toml:"-"`
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

func NewFromPlatformConfig(raw map[string]any, store storage.Store, chatHistory storage.ChatHistoryRepository, logger *slog.Logger, superadmins []string, commandPrefixes []string, attachmentDir string, maxReceiveFileBytes int64, downloadTimeoutSecs int) (*Adapter, error) {
	var cfg Config
	if err := platform.DecodeConfig(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode qqonebot config: %w", err)
	}
	cfg.Superadmins = superadmins
	cfg.CommandPrefixes = append([]string(nil), commandPrefixes...)
	cfg.AttachmentDir = strings.TrimSpace(attachmentDir)
	cfg.MaxReceiveFileBytes = maxReceiveFileBytes
	cfg.DownloadTimeoutSecs = downloadTimeoutSecs
	applyDefaults(&cfg)
	if err := validateSendFileMode(cfg.SendFileMode); err != nil {
		return nil, err
	}
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
	cfg.SendFileMode = strings.ToLower(strings.TrimSpace(cfg.SendFileMode))
	if cfg.SendFileMode == "" {
		cfg.SendFileMode = sendFileModeBase64
	}
	if len(cfg.CommandPrefixes) == 0 {
		cfg.CommandPrefixes = []string{"/"}
	}
	if cfg.MaxReceiveFileBytes <= 0 {
		cfg.MaxReceiveFileBytes = 100 * 1024 * 1024
	}
	if cfg.DownloadTimeoutSecs <= 0 {
		cfg.DownloadTimeoutSecs = 60
	}
}

func validateSendFileMode(mode string) error {
	switch mode {
	case sendFileModeBase64, sendFileModeFileURI:
		return nil
	default:
		return fmt.Errorf("qqonebot send_file_mode must be %q or %q, got %q", sendFileModeBase64, sendFileModeFileURI, mode)
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
	backoff := platform.NewBackoff(interval, 10*time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.transport.Connect(ctx); err != nil {
			if backoff.ShouldWarn() {
				a.logWarn("onebot connect failed", "error", err)
			}
			if !sleepContext(ctx, backoff.Delay()) {
				return ctx.Err()
			}
			continue
		}
		backoff.Reset()
		a.logInfo("onebot connected", "url", a.cfg.URL)
		go a.notifyConnected(ctx)
		err := a.readLoop(ctx, handler)
		a.transport.Close(websocket.StatusNormalClosure, "reconnect")
		if err != nil && !errors.Is(err, context.Canceled) {
			a.logWarn("onebot disconnected", "error", err)
		}
		if !sleepContext(ctx, backoff.Delay()) {
			return ctx.Err()
		}
	}
}

func (a *Adapter) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	if text, ok := textOutputs(outputs); ok {
		return a.sendContextText(ctx, text)
	}
	t, ok := ctx.Value(targetKey{}).(target)
	if !ok {
		return delivery.Receipt{}, fmt.Errorf("qq send target missing")
	}
	segments, err := outputSegments(a.cfg.SendFileMode, outputs...)
	if err != nil {
		return delivery.Receipt{}, err
	}
	return a.sendSegments(ctx, t, segments)
}

func (a *Adapter) CallPlatformAPI(ctx context.Context, api string, params map[string]any) (json.RawMessage, error) {
	if a.transport == nil {
		return nil, fmt.Errorf("qqonebot transport is not configured")
	}
	resp, err := a.transport.Call(ctx, strings.TrimSpace(api), params)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (a *Adapter) SendNotice(ctx context.Context, outTarget delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	if outTarget.Empty() && isGroupToolPreviewNotice(ctx, outputs) {
		return delivery.Receipt{}, nil
	}
	if outTarget.Empty() {
		return a.SendChat(ctx, outputs)
	}
	if outTarget.Superadmins {
		if len(a.cfg.Superadmins) == 0 {
			return delivery.Receipt{}, fmt.Errorf("qqonebot superadmins are not configured")
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
			sent, err := a.SendNotice(ctx, copyTarget, outputs)
			if err != nil {
				return delivery.Receipt{}, err
			}
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
		}
		return receipt, nil
	}
	t, err := targetToQQ(outTarget)
	if err != nil {
		return delivery.Receipt{}, err
	}
	segments, err := outputSegments(a.cfg.SendFileMode, outputs...)
	if err != nil {
		return delivery.Receipt{}, err
	}
	return a.sendSegments(ctx, t, segments)
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
func isGroupToolPreviewNotice(ctx context.Context, outputs []delivery.Output) bool {
	if len(outputs) != 1 || outputs[0].Kind != delivery.KindText || !strings.HasPrefix(strings.TrimSpace(outputs[0].Text), "[tool]") {
		return false
	}
	t, ok := ctx.Value(targetKey{}).(target)
	return ok && t.MessageType == "group"
}

func (a *Adapter) sendContextText(ctx context.Context, text string) (delivery.Receipt, error) {
	if strings.TrimSpace(text) == "" {
		return delivery.Receipt{}, nil
	}
	t, ok := ctx.Value(targetKey{}).(target)
	if !ok {
		return delivery.Receipt{}, fmt.Errorf("qq send target missing")
	}
	var receipt delivery.Receipt
	for _, page := range qqTextPages(text) {
		id, err := a.sendQQText(ctx, t, page)
		if err != nil {
			return delivery.Receipt{}, err
		}
		if strings.TrimSpace(id) != "" {
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, id)
		}
	}
	return receipt, nil
}

func (a *Adapter) sendContextOutput(ctx context.Context, out delivery.Output) (delivery.Receipt, error) {
	return a.SendChat(ctx, []delivery.Output{out})
}

func (a *Adapter) sendSegments(ctx context.Context, t target, segments []Segment) (delivery.Receipt, error) {
	switch t.MessageType {
	case "private":
		id, err := a.transport.SendPrivateSegments(ctx, t.UserID, segments)
		return receiptWithMessageID(id), err
	case "group":
		id, err := a.transport.SendGroupSegments(ctx, t.GroupID, segments)
		return receiptWithMessageID(id), err
	default:
		return delivery.Receipt{}, fmt.Errorf("unsupported message target %q", t.MessageType)
	}
}

func (a *Adapter) sendTarget(ctx context.Context, outTarget delivery.Target, out delivery.Output) (delivery.Receipt, error) {
	return a.SendNotice(ctx, outTarget, []delivery.Output{out})
}

func receiptWithMessageID(id string) delivery.Receipt {
	id = strings.TrimSpace(id)
	if id == "" {
		return delivery.Receipt{}
	}
	return delivery.Receipt{PlatformMessageIDs: []string{id}}
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
			if replyID == "" {
				return nil, fmt.Errorf("reply target message id is empty")
			}
			segments = append(segments, Segment{Type: "reply", Data: map[string]any{"id": replyID}}, Segment{Type: "text", Data: map[string]any{"text": out.Text}})
		case delivery.KindEmoticon:
			id := strings.TrimSpace(out.EmoticonID)
			if id == "" {
				return nil, fmt.Errorf("emoticon id is empty")
			}
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
			if qq == "" {
				qq = strings.TrimSpace(out.Text)
			}
			if qq == "" {
				return nil, fmt.Errorf("at target is empty")
			}
			segments = append(segments, Segment{Type: "at", Data: map[string]any{"qq": qq}})
		default:
			return nil, fmt.Errorf("unsupported output kind %q", out.Kind)
		}
	}
	return segments, nil
}

func oneBotGroupRole(event Event) security.GroupRole {
	if event.MessageType != "group" {
		return security.GroupRoleUnknown
	}
	return security.ParseGroupRole(event.Sender.Role)
}

func oneBotSourceFile(source delivery.Source, label, sendFileMode string) (string, error) {
	if len(source.Data) > 0 {
		return "base64://" + base64.StdEncoding.EncodeToString(source.Data), nil
	}
	if sourceURL := strings.TrimSpace(source.URL); sourceURL != "" {
		return sourceURL, nil
	}
	path := strings.TrimSpace(source.Path)
	if path == "" {
		return "", fmt.Errorf("%s path is empty", label)
	}
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

func (a *Adapter) readLoop(ctx context.Context, handler platform.PlatformHandler) error {
	for {
		event, err := a.transport.Read(ctx)
		if err != nil {
			return err
		}
		if event.PostType == "" {
			continue
		}
		if a.isMessageEvent(event) {
			go a.handleEvent(ctx, handler, event)
			continue
		}
	}
}

func (a *Adapter) handleEvent(ctx context.Context, handler platform.PlatformHandler, event Event) {
	normalized := normalizeMessage(event.Message, event.RawMessage, event.SelfID)
	normalized = a.resolveAtSegments(ctx, event, normalized)
	a.recordChatMessage(ctx, event, normalized)
	if event.MessageType != "private" && event.MessageType != "group" {
		return
	}
	text := normalized.Text
	currentSegments := a.resolveImageSegments(ctx, normalized.Segments)
	attachments := inboundAttachments{Segments: currentSegments}
	if a.shouldAutoReceivePrivateFile(event) {
		attachments = a.prepareInboundAttachments(ctx, currentSegments)
		currentSegments = attachments.Segments
	}
	messageCtx := platform.MessageContext{
		Platform:              a.Name(),
		PlatformUserID:        strconv.FormatInt(event.UserID, 10),
		Nickname:              strings.TrimSpace(event.Sender.Nickname),
		GroupCard:             strings.TrimSpace(event.Sender.Card),
		DisplayName:           displayName(event.Sender, event.UserID),
		GroupRole:             oneBotGroupRole(event),
		ScopeID:               scopeID(event),
		ConversationKind:      oneBotConversationKind(event),
		PlatformMessageID:     strconv.FormatInt(event.MessageID, 10),
		ReplyToMessageID:      normalized.ReplyID,
		ReplyToSenderID:       a.replyToSenderID(ctx, normalized.ReplyID),
		Sender:                a,
		BufferAssistantOutput: true,
		Segments:              finalMessageSegments(text, currentSegments, nil),
		RawText:               normalized.Text,
		PlatformMessage:       append(json.RawMessage(nil), event.Message...),
		Bot:                   platform.Identity{UserID: strconv.FormatInt(event.SelfID, 10)},
		Mentions:              append([]platform.Mention(nil), normalized.Mentions...),
		TriggerKeywords:       append([]string(nil), a.cfg.TriggerKeywords...),
		Meta: map[string]any{
			"qq_onebot.message_id":   strconv.FormatInt(event.MessageID, 10),
			"qq_onebot.message_type": event.MessageType,
			"qq_onebot.group_id":     strconv.FormatInt(event.GroupID, 10),
			"qq_onebot.user_id":      strconv.FormatInt(event.UserID, 10),
		},
	}
	msgCtx := platform.WithMessageContext(ctx, messageCtx)
	msgCtx = context.WithValue(msgCtx, targetKey{}, target{MessageType: event.MessageType, UserID: event.UserID, GroupID: event.GroupID})

	var referenceSegments []platform.MessageSegment
	if normalized.ReplyID != "" {
		ref := refcontext.Apply(msgCtx, refcontext.Options{
			Store:           a.store,
			Platform:        a.Name(),
			ScopeID:         messageCtx.ScopeID,
			ActorID:         security.ActorID(a.Name(), strconv.FormatInt(event.UserID, 10)),
			IsSuperadmin:    isConfiguredSuperadmin(a.cfg.Superadmins, strconv.FormatInt(event.UserID, 10)),
			ReplyID:         normalized.ReplyID,
			Text:            text,
			CommandPrefixes: a.cfg.CommandPrefixes,
			Fetch:           a.referenceFetcher(event),
		})
		messageCtx.ForkFromMessageID = ref.ForkFromMessageID
		messageCtx.ResumeSessionID = ref.ResumeSessionID
		messageCtx.ContextText = ref.Text
		messageCtx.Reply = ref.Reply
		referenceSegments = ref.ReferenceSegments
		if strings.TrimSpace(ref.Text) != "" {
			messageCtx.ContextSegments = finalMessageSegments(ref.Text, currentSegments, referenceSegments)
		}
	}
	messageCtx.Segments = finalMessageSegments(text, currentSegments, nil)
	msgCtx = platform.WithMessageContext(ctx, messageCtx)
	msgCtx = context.WithValue(msgCtx, targetKey{}, target{MessageType: event.MessageType, UserID: event.UserID, GroupID: event.GroupID})
	if len(attachments.TooLarge) > 0 {
		if _, err := a.SendChat(msgCtx, []delivery.Output{platformTooLargeAttachmentsOutput(attachments.TooLarge, a.cfg.MaxReceiveFileBytes)}); err != nil {
			a.logWarn("send onebot attachment too large notice failed", "error", err, "message_id", event.MessageID)
		}
	}
	if !hasTextSegment(currentSegments) && !hasPlatformImageSegment(currentSegments) {
		if len(attachments.TooLarge) > 0 {
			return
		}
		if len(attachments.Saved) > 0 {
			if _, err := a.SendChat(msgCtx, []delivery.Output{platformSavedAttachmentsOutput(attachments.Saved)}); err != nil {
				a.logWarn("send onebot attachment saved notice failed", "error", err, "message_id", event.MessageID)
			}
			return
		}
		return
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	if err := handler.HandleMessage(msgCtx, text); err != nil {
		a.logWarn("handle qq message failed", "error", err, "message_id", event.MessageID)
	}
}

func (a *Adapter) resolveAtSegments(ctx context.Context, event Event, msg NormalizedMessage) NormalizedMessage {
	if event.MessageType != "group" || event.GroupID == 0 || a.transport == nil || len(msg.Segments) == 0 {
		return msg
	}
	updated := append([]platform.MessageSegment(nil), msg.Segments...)
	replacements := map[string]string{}
	changed := false
	for i := range updated {
		if updated[i].Type != platform.SegmentAt || updated[i].UserID == "" {
			continue
		}
		userID, err := strconv.ParseInt(updated[i].UserID, 10, 64)
		if err != nil || userID == event.SelfID {
			continue
		}
		sender, err := a.transport.GetGroupMemberInfo(ctx, event.GroupID, userID)
		if err != nil {
			a.logWarn("get qq group member info failed", "group_id", event.GroupID, "user_id", userID, "error", err)
			continue
		}
		text := atText(updated[i].UserID, senderName(sender))
		if text == updated[i].Text {
			continue
		}
		replacements[updated[i].Text] = text
		updated[i].Text = text
		changed = true
	}
	if !changed {
		return msg
	}
	for oldText, newText := range replacements {
		msg.Text = strings.ReplaceAll(msg.Text, oldText, newText)
	}
	msg.Segments = updated
	return msg
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

func oneBotConversationKind(event Event) platform.ConversationKind {
	switch event.MessageType {
	case "private":
		return platform.ConversationPrivate
	case "group":
		return platform.ConversationGroup
	default:
		return platform.ConversationUnknown
	}
}

func (a *Adapter) replyToSenderID(ctx context.Context, replyID string) string {
	replyID = strings.TrimSpace(replyID)
	if replyID == "" || a.transport == nil {
		return ""
	}
	data, err := a.transport.GetMessage(ctx, replyID)
	if err != nil || data.UserID == 0 {
		return ""
	}
	return strconv.FormatInt(data.UserID, 10)
}

func (a *Adapter) referenceFetcher(event Event) func(context.Context, string) (refcontext.ReferencedMessage, bool) {
	return func(ctx context.Context, replyID string) (refcontext.ReferencedMessage, bool) {
		if a.transport == nil {
			return refcontext.ReferencedMessage{}, false
		}
		data, err := a.transport.GetMessage(ctx, replyID)
		if err != nil {
			return refcontext.ReferencedMessage{}, false
		}
		ref := normalizeMessage(data.Message, data.RawMessage, event.SelfID)
		label := "引用"
		if data.UserID != 0 {
			label = "引用：" + displayName(data.Sender, data.UserID)
		}
		return refcontext.ReferencedMessage{SenderID: strconv.FormatInt(data.UserID, 10), Label: label, Text: ref.Text, Segments: a.resolveImageSegments(ctx, ref.Segments)}, true
	}
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
		SenderName:               senderName(event.Sender),
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

func hasPlatformImageSegment(segments []platform.MessageSegment) bool {
	for _, segment := range segments {
		if segment.Type == platform.SegmentImage {
			return true
		}
	}
	return false
}

func hasTextSegment(segments []platform.MessageSegment) bool {
	for _, segment := range segments {
		if segment.Type == platform.SegmentText && strings.TrimSpace(segment.Text) != "" {
			return true
		}
	}
	return false
}

func (a *Adapter) isMessageEvent(event Event) bool {
	return event.PostType == "message" && (event.MessageType == "private" || event.MessageType == "group")
}

func (a *Adapter) shouldAutoReceivePrivateFile(event Event) bool {
	if event.MessageType != "private" {
		return false
	}
	return isConfiguredSuperadmin(a.cfg.Superadmins, strconv.FormatInt(event.UserID, 10))
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

func isConfiguredSuperadmin(superadmins []string, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, candidate := range superadmins {
		candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "qqonebot:"))
		if candidate == id {
			return true
		}
	}
	return false
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
