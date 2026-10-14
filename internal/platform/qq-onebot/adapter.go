package qqonebot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
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
	AccessTokenEnv           string   `toml:"access_token_env"`
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
		pages = append(pages, string(runes[start:end]))
	}
	return pages
}

type Adapter struct {
	cfg         Config
	store       storage.Store
	chatHistory storage.ChatHistoryRepository
	transport   *Transport
}

type target struct {
	MessageType string
	UserID      int64
	GroupID     int64
}

func NewFromPlatformConfig(raw map[string]any, store storage.Store, chatHistory storage.ChatHistoryRepository, superadmins []string, commandPrefixes []string, configEnvDir, attachmentDir string, maxReceiveFileBytes int64, downloadTimeoutSecs int) (*Adapter, error) {
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
	envName := strings.TrimSpace(cfg.AccessTokenEnv)
	if strings.TrimSpace(cfg.AccessToken) == "" && envName != "" {
		value, _, err := config.ConfigEnv(envName, configEnvDir)
		if err != nil {
			return nil, fmt.Errorf("resolve qqonebot access token from %s: %w", envName, err)
		}
		cfg.AccessToken = strings.TrimSpace(value)
	}
	return New(cfg, store, chatHistory), nil
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

func New(cfg Config, store storage.Store, chatHistory storage.ChatHistoryRepository) *Adapter {
	applyDefaults(&cfg)

	timeout := time.Duration(cfg.APITimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Adapter{cfg: cfg, store: store, chatHistory: chatHistory, transport: &Transport{
		URL:         cfg.URL,
		AccessToken: cfg.AccessToken,
		Timeout:     timeout,
	}}
}

func (a *Adapter) Name() string { return "qqonebot" }

func (a *Adapter) Enabled() bool { return a.cfg.Enabled }

func (a *Adapter) notifyConnected(ctx context.Context) {
	// Subscribers hand background work to their own business queues.
	_, _ = globalevents.PlatformConnected.Emit(ctx, globalevents.PlatformConnectedEvent{Platform: a.Name()})
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
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelWarn,
						EventName:    "onebot_connect_failed",
						Module:       "qq-onebot",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("onebot connect failed\nerror=%v", err),
						ContentLimit: 4096,
						ResultStatus: globalevents.ResultFailed,
					}
					if errors.Is(err, context.Canceled) {
						record.ResultStatus = globalevents.ResultCanceled
						record.Level = slog.LevelInfo
					}
					facts, _ := contextinfo.ExecutionFromContext(ctx)
					if facts.SessionID != "" {
						record.Identity.SessionID = &facts.SessionID
					}
					if facts.RunID != "" {
						record.Identity.RunID = &facts.RunID
					}
					if facts.Attempt != "" {
						record.Identity.Attempt = &facts.Attempt
					}
					if facts.RequestID != "" {
						record.Identity.RequestID = &facts.RequestID
					}
					if facts.RootRequestID != "" {
						record.Identity.RootRequestID = &facts.RootRequestID
					}
					_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
				}
			}
			if !sleepContext(ctx, backoff.Delay()) {
				return ctx.Err()
			}
			continue
		}
		backoff.Reset()
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "onebot_connected",
				Module:       "qq-onebot",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("onebot connected\nurl=%v", a.cfg.URL),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSucceeded,
			}
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
		}
		a.notifyConnected(ctx)
		err := a.readLoop(ctx, handler)
		a.transport.Close(websocket.StatusNormalClosure, "reconnect")
		if err != nil && !errors.Is(err, context.Canceled) {
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelWarn,
					EventName:    "onebot_disconnected",
					Module:       "qq-onebot",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("onebot disconnected\nerror=%v", err),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				facts, _ := contextinfo.ExecutionFromContext(ctx)
				if facts.SessionID != "" {
					record.Identity.SessionID = &facts.SessionID
				}
				if facts.RunID != "" {
					record.Identity.RunID = &facts.RunID
				}
				if facts.Attempt != "" {
					record.Identity.Attempt = &facts.Attempt
				}
				if facts.RequestID != "" {
					record.Identity.RequestID = &facts.RequestID
				}
				if facts.RootRequestID != "" {
					record.Identity.RootRequestID = &facts.RootRequestID
				}
				_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
			}
		}
		if !sleepContext(ctx, backoff.Delay()) {
			return ctx.Err()
		}
	}
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

func (a *Adapter) CallPlatformAPI(ctx context.Context, api string, params map[string]any) (json.RawMessage, error) {
	resp, err := a.transport.Call(ctx, strings.TrimSpace(api), params)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (a *Adapter) sendTemporary(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	transport := &Transport{URL: a.cfg.URL, AccessToken: a.cfg.AccessToken, Timeout: time.Duration(a.cfg.APITimeoutSeconds) * time.Second}
	if err := transport.Connect(ctx); err != nil {
		{
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return delivery.Receipt{}, logResult, err
		}
	}
	defer transport.Close(websocket.StatusNormalClosure, "temporary elnis delivery done")
	go transport.readResponses(ctx)
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

func (a *Adapter) sendContextOutput(ctx context.Context, out delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	return a.SendChat(ctx, []delivery.Output{out})
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

func (a *Adapter) sendTarget(ctx context.Context, outTarget delivery.Target, out delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	return a.SendTo(ctx, outTarget, []delivery.Output{out})
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

func oneBotGroupRole(event Event) contextinfo.GroupRole {
	if event.MessageType != "group" {
		return contextinfo.GroupRoleUnknown
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
	if event.MessageType != "private" && event.MessageType != "group" {
		a.recordChatMessage(ctx, event, normalized, platform.ReplyContext{})
		return
	}
	replyToSenderID, err := a.replyToSenderID(ctx, event, normalized.ReplyID)
	if err != nil {
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelError,
			EventName:    "platform_context_failed",
			Module:       "qq-onebot",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("prepare platform context failed: %v", err),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultFailed,
		}
		_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
		return
	}
	text := normalized.Text
	currentSegments := normalized.Segments
	conversationID := strconv.FormatInt(event.UserID, 10)
	if event.MessageType == "group" {
		conversationID = strconv.FormatInt(event.GroupID, 10)
	}
	messageCtx := platform.MessageContext{
		Conversation: contextinfo.Conversation{
			Source: contextinfo.Source{
				Platform:         a.Name(),
				ScopeID:          scopeID(event),
				ConversationKind: oneBotConversationKind(event),
				ConversationID:   conversationID,
			},
			Identity: contextinfo.Identity{
				ActorID:        security.ActorID(a.Name(), strconv.FormatInt(event.UserID, 10)),
				PlatformUserID: strconv.FormatInt(event.UserID, 10),
				Nickname:       strings.TrimSpace(event.Sender.Nickname),
				GroupCard:      strings.TrimSpace(event.Sender.Card),
				DisplayName:    displayName(event.Sender, event.UserID),
			},
			PlatformMessageID: strconv.FormatInt(event.MessageID, 10),
			ReplyToMessageID:  normalized.ReplyID,
			ReplyToSenderID:   replyToSenderID,
		},
		GroupRole:             oneBotGroupRole(event),
		MediaResolver:         a,
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

	var currentDisplay []platform.MessageSegment
	if event.MessageType == "private" && !platform.HasCommandPrefix(text, a.cfg.CommandPrefixes) {
		if segments, ok := decodeMessageSegments(event.Message); ok {
			var err error
			currentDisplay, err = a.expandForwardSegments(msgCtx, segments, event.SelfID, currentSegments)
			if err != nil {
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelError,
					EventName:    "platform_context_failed",
					Module:       "qq-onebot",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("prepare platform context failed: %v", err),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				_ = globalevents.EmitLog(context.WithoutCancel(msgCtx), record)
				return
			}
		}
	}
	// An expanded current message is composed after the reference view. Commands
	// keep the original path so refcontext can still interpret /fork and others.
	referenceInputText, referenceInputSegments := text, currentSegments
	if len(currentDisplay) > 0 {
		referenceInputText, referenceInputSegments = "", nil
	}
	var referenceSegments []platform.MessageSegment
	if normalized.ReplyID != "" {
		ref, err := refcontext.Apply(msgCtx, refcontext.Options{
			Store:           a.store,
			ChatHistory:     a.chatHistory,
			Platform:        a.Name(),
			ScopeID:         messageCtx.Conversation.Source.ScopeID,
			ActorID:         security.ActorID(a.Name(), strconv.FormatInt(event.UserID, 10)),
			IsSuperadmin:    isConfiguredSuperadmin(a.cfg.Superadmins, strconv.FormatInt(event.UserID, 10)),
			ReplyID:         normalized.ReplyID,
			Text:            referenceInputText,
			CommandPrefixes: a.cfg.CommandPrefixes,
			Fetch:           a.referenceFetcher(event),
			Enrich:          a.expandForwardReference(event),
		})
		if err != nil {
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "platform_context_failed",
				Module:       "qq-onebot",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("prepare platform context failed: %v", err),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			_ = globalevents.EmitLog(context.WithoutCancel(msgCtx), record)
			return
		}
		messageCtx.ForkFromMessageID = ref.ForkFromMessageID
		messageCtx.ResumeSessionID = ref.ResumeSessionID
		messageCtx.ContextText = ref.Text
		messageCtx.Reply = ref.Reply
		referenceSegments = ref.ReferenceSegments
		if len(ref.DisplaySegments) > 0 {
			messageCtx.ContextSegments = forwardContextSegments(ref.DisplaySegments, referenceInputSegments, referenceInputText)
		} else if strings.TrimSpace(ref.Text) != "" || len(referenceSegments) > 0 {
			messageCtx.ContextSegments = finalMessageSegments(ref.Text, referenceInputSegments, referenceSegments)
		}
	}
	if len(currentDisplay) > 0 {
		messageCtx.ContextSegments = forwardContextSegments(messageCtx.ContextSegments, currentDisplay, "")
		var displayText strings.Builder
		for _, segment := range messageCtx.ContextSegments {
			if segment.Type == platform.SegmentText {
				displayText.WriteString(segment.Text)
			}
		}
		messageCtx.ContextText = displayText.String()
	}
	a.recordChatMessage(ctx, event, normalized, messageCtx.Reply)
	messageCtx.Segments = finalMessageSegments(text, currentSegments, nil)
	msgCtx = platform.WithMessageContext(ctx, messageCtx)
	if strings.TrimSpace(text) == "" && len(currentSegments) == 0 {
		return
	}
	_ = handler.HandleMessage(msgCtx, text)
}

func (a *Adapter) resolveAtSegments(ctx context.Context, event Event, msg NormalizedMessage) NormalizedMessage {
	if event.MessageType != "group" || event.GroupID == 0 || len(msg.Segments) == 0 {
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
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelWarn,
					EventName:    "get_qq_group_member_info_failed",
					Module:       "qq-onebot",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("get qq group member info failed\ngroup_id=%v\nuser_id=%v\nerror=%v", event.GroupID, userID, err),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				if errors.Is(err, context.Canceled) {
					record.ResultStatus = globalevents.ResultCanceled
					record.Level = slog.LevelInfo
				}
				facts, _ := contextinfo.ExecutionFromContext(ctx)
				if facts.SessionID != "" {
					record.Identity.SessionID = &facts.SessionID
				}
				if facts.RunID != "" {
					record.Identity.RunID = &facts.RunID
				}
				if facts.Attempt != "" {
					record.Identity.Attempt = &facts.Attempt
				}
				if facts.RequestID != "" {
					record.Identity.RequestID = &facts.RequestID
				}
				if facts.RootRequestID != "" {
					record.Identity.RootRequestID = &facts.RootRequestID
				}
				_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
			}
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

func oneBotConversationKind(event Event) contextinfo.ConversationKind {
	switch event.MessageType {
	case "private":
		return contextinfo.ConversationPrivate
	case "group":
		return contextinfo.ConversationGroup
	default:
		return contextinfo.ConversationUnknown
	}
}

func (a *Adapter) replyToSenderID(ctx context.Context, event Event, replyID string) (string, error) {
	replyID = strings.TrimSpace(replyID)
	if replyID == "" {
		return "", nil
	}
	msg, err := a.store.Messages().FindByPlatformMessage(ctx, a.Name(), scopeID(event), replyID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return "", err
	}
	if err == nil && msg.Role == storage.RoleAssistant {
		return strconv.FormatInt(event.SelfID, 10), nil
	}
	outputs, err := a.store.Media().FindOutputs(ctx, a.Name(), scopeID(event), replyID, time.Now())
	if err != nil {
		return "", err
	}
	if len(outputs) > 0 {
		return strconv.FormatInt(event.SelfID, 10), nil
	}
	history, err := a.chatHistory.GetByPlatformMessage(ctx, a.Name(), scopeID(event), replyID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return "", err
	}
	if err == nil {
		return history.SenderID, nil
	}
	data, err := a.transport.GetMessage(ctx, replyID)
	if err != nil {
		return "", err
	}
	if data.UserID == 0 {
		return "", nil
	}
	return strconv.FormatInt(data.UserID, 10), nil
}

func (a *Adapter) referenceFetcher(event Event) func(context.Context, string) (refcontext.ReferencedMessage, bool, error) {
	return func(ctx context.Context, replyID string) (refcontext.ReferencedMessage, bool, error) {
		data, err := a.transport.GetMessage(ctx, replyID)
		if err != nil {
			return refcontext.ReferencedMessage{}, false, err
		}
		ref := normalizeMessage(data.Message, data.RawMessage, event.SelfID)
		label := "引用"
		if data.UserID != 0 {
			label = "引用：" + displayName(data.Sender, data.UserID)
		}
		return refcontext.ReferencedMessage{SenderID: strconv.FormatInt(data.UserID, 10), SenderName: displayName(data.Sender, data.UserID), Label: label, Text: ref.Text, Segments: ref.Segments}, true, nil
	}
}

func (a *Adapter) recordChatMessage(ctx context.Context, event Event, normalized NormalizedMessage, reply platform.ReplyContext) {
	if (strings.TrimSpace(normalized.Text) == "" && len(normalized.Segments) == 0) || event.MessageID == 0 {
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
		Raw:                      normalized.Text,
		Segments:                 platform.MarshalChatSegments(normalized.Segments),
		ReplyToPlatformMessageID: normalized.ReplyID,
		Metadata:                 refcontext.MarshalChatMetadata(reply),
		CreatedAt:                createdAt,
	}
	if err := a.chatHistory.Append(ctx, message); err != nil {
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "record_qq_chat_message_failed",
				Module:       "qq-onebot",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("record qq chat message failed\nerror=%v\nmessage_id=%v", err, event.MessageID),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			if errors.Is(err, context.Canceled) {
				record.ResultStatus = globalevents.ResultCanceled
				record.Level = slog.LevelInfo
			}
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
		}
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
