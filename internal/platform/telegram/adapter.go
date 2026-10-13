package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/platform/refcontext"
	"elbot/internal/security"
	"elbot/internal/storage"
)

type Adapter struct {
	cfg         Config
	store       storage.Store
	chatHistory storage.ChatHistoryRepository
	client      *apiClient

	botID          int64
	botUsername    string
	commandCatalog []command.Info
}

type target struct {
	ChatID  int64
	ScopeID string
}

func New(cfg Config, store storage.Store, chatHistory storage.ChatHistoryRepository) *Adapter {
	applyDefaults(&cfg)
	return &Adapter{cfg: cfg, store: store, chatHistory: chatHistory, client: newAPIClient(cfg)}
}

func (a *Adapter) Name() string { return platformName }

func (a *Adapter) Enabled() bool { return a.cfg.Enabled }

func (a *Adapter) SetCommandCatalog(infos []command.Info) {
	a.commandCatalog = append([]command.Info(nil), infos...)
}

func (a *Adapter) notifyConnected(ctx context.Context) {
	// Subscribers hand background work to their own business queues.
	_, _ = globalevents.PlatformConnected.Emit(ctx, globalevents.PlatformConnectedEvent{Platform: a.Name()})
}

func (a *Adapter) Run(ctx context.Context, handler platform.PlatformHandler) error {
	if !a.cfg.Enabled {
		return nil
	}
	backoff := platform.NewBackoff(a.cfg.reconnectInterval(), 10*time.Second)
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		me, err := a.client.getMe(ctx)
		if err != nil {
			if backoff.ShouldWarn() {
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelWarn,
						EventName:    "telegram_getme_failed",
						Module:       "telegram",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("telegram getMe failed\nerror=%v", err),
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
		a.botID = me.ID
		a.botUsername = strings.TrimSpace(me.Username)
		if err := a.syncBotCommands(ctx); err != nil {
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelError,
					EventName:    "sync_telegram_bot_commands_failed",
					Module:       "telegram",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("sync telegram bot commands failed\nerror=%v", err),
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
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelInfo,
				EventName:    "telegram_connected",
				Module:       "telegram",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("telegram connected\nbot_id=%v\nbot_username=%v", me.ID, me.Username),
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
		for {
			updates, err := a.client.getUpdates(ctx, offset)
			if err != nil {
				if errors.Is(err, context.Canceled) || ctx.Err() != nil {
					return ctx.Err()
				}
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelWarn,
						EventName:    "telegram_getupdates_failed",
						Module:       "telegram",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("telegram getUpdates failed\nerror=%v", err),
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
				break
			}
			for _, upd := range updates {
				if upd.UpdateID >= offset {
					offset = upd.UpdateID + 1
				}
				go a.handleUpdate(ctx, handler, upd)
			}
		}
		if !sleepContext(ctx, backoff.Delay()) {
			return ctx.Err()
		}
	}
}

func (a *Adapter) handleUpdate(ctx context.Context, handler platform.PlatformHandler, upd update) {
	if upd.CallbackQuery != nil {
		a.handleCallbackQuery(ctx, handler, *upd.CallbackQuery)
		return
	}
	if upd.Message == nil {
		return
	}
	a.handleMessage(ctx, handler, *upd.Message)
}

func (a *Adapter) messageGroupRole(ctx context.Context, msg message) contextinfo.GroupRole {
	if msg.From == nil || (msg.Chat.Type != "group" && msg.Chat.Type != "supergroup") {
		return contextinfo.GroupRoleUnknown
	}
	member, err := a.client.getChatMember(ctx, msg.Chat.ID, msg.From.ID)
	if err != nil {
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelWarn,
				EventName:    "get_telegram_chat_member_failed",
				Module:       "telegram",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("get telegram chat member failed\nerror=%v", err),
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
		return contextinfo.GroupRoleUnknown
	}
	switch strings.TrimSpace(member.Status) {
	case "creator":
		return contextinfo.GroupRoleOwner
	case "administrator":
		return contextinfo.GroupRoleAdmin
	case "member", "restricted":
		return contextinfo.GroupRoleMember
	default:
		return contextinfo.GroupRoleUnknown
	}
}

func (a *Adapter) handleCallbackQuery(ctx context.Context, handler platform.PlatformHandler, query callbackQuery) {
	if strings.TrimSpace(query.ID) != "" {
		if err := a.client.answerCallbackQuery(ctx, query.ID, "已收到"); err != nil {
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelError,
					EventName:    "answer_telegram_callback_failed",
					Module:       "telegram",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("answer telegram callback failed\nerror=%v", err),
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
	if query.Message == nil || strings.TrimSpace(query.Data) == "" {
		return
	}
	msg := *query.Message
	msg.From = &query.From
	msg.Text = strings.TrimSpace(query.Data)
	a.handleMessage(ctx, handler, msg)
}

func (a *Adapter) handleMessage(ctx context.Context, handler platform.PlatformHandler, msg message) {
	normalized := normalizeMessage(msg)
	if msg.Chat.Type != "private" && msg.Chat.Type != "group" && msg.Chat.Type != "supergroup" {
		a.recordChatMessage(ctx, msg, normalized, platform.ReplyContext{})
		return
	}
	text := normalized.Text
	platformUserID := userIDString(msg.From)
	groupRole := a.messageGroupRole(ctx, msg)
	messageCtx := platform.MessageContext{
		Conversation: contextinfo.Conversation{
			Source: contextinfo.Source{
				Platform:         a.Name(),
				ScopeID:          scopeID(msg.Chat),
				ConversationKind: telegramConversationKind(msg.Chat),
				ConversationID:   formatMessageID(msg.Chat.ID),
			},
			Identity: contextinfo.Identity{
				ActorID:        security.ActorID(a.Name(), platformUserID),
				PlatformUserID: platformUserID,
				Nickname:       displayNamePtr(msg.From, ""),
				DisplayName:    displayNamePtr(msg.From, ""),
			},
			PlatformMessageID: formatMessageID(msg.MessageID),
			ReplyToMessageID:  normalized.ReplyID,
			ReplyToSenderID:   userIDString(replySender(normalized.ReplyMessage)),
		},
		GroupRole: groupRole,

		Sender:          a,
		MediaResolver:   a,
		Segments:        finalMessageSegments(text, normalized.Segments, nil),
		RawText:         normalized.Text,
		Bot:             platform.Identity{UserID: formatMessageID(a.botID), Username: a.botUsername},
		Mentions:        append([]platform.Mention(nil), normalized.Mentions...),
		TriggerKeywords: append([]string(nil), a.cfg.TriggerKeywords...),
		Meta: map[string]any{
			"telegram.message_id": formatMessageID(msg.MessageID),
			"telegram.chat_id":    strconv.FormatInt(msg.Chat.ID, 10),
		},
	}
	msgCtx := platform.WithMessageContext(ctx, messageCtx)

	var referenceSegments []platform.MessageSegment
	if normalized.ReplyID != "" {
		ref := refcontext.Apply(msgCtx, refcontext.Options{
			Store:           a.store,
			ChatHistory:     a.chatHistory,
			Platform:        a.Name(),
			ScopeID:         messageCtx.Conversation.Source.ScopeID,
			ActorID:         messageCtx.Conversation.Identity.ActorID,
			IsSuperadmin:    isConfiguredSuperadmin(a.cfg.Superadmins, userIDString(msg.From)),
			ReplyID:         normalized.ReplyID,
			Text:            text,
			CommandPrefixes: a.cfg.CommandPrefixes,
			Fetch:           a.referenceFetcher(msg, normalized),
		})
		messageCtx.ForkFromMessageID = ref.ForkFromMessageID
		messageCtx.ResumeSessionID = ref.ResumeSessionID
		messageCtx.ContextText = ref.Text
		messageCtx.Reply = ref.Reply
		referenceSegments = ref.ReferenceSegments
		if strings.TrimSpace(ref.Text) != "" || len(referenceSegments) > 0 {
			messageCtx.ContextSegments = finalMessageSegments(ref.Text, normalized.Segments, referenceSegments)
		}
	}
	a.recordChatMessage(ctx, msg, normalized, messageCtx.Reply)
	if strings.TrimSpace(text) == "" && strings.TrimSpace(messageCtx.ContextText) == "" && len(messageCtx.ContextSegments) == 0 && len(normalized.Segments) == 0 {
		return
	}
	messageCtx.Segments = finalMessageSegments(text, normalized.Segments, nil)
	msgCtx = platform.WithMessageContext(ctx, messageCtx)
	_ = handler.HandleMessage(msgCtx, text)
}

func (a *Adapter) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	return a.sendContextOutput(ctx, outputs)
}

func (a *Adapter) CallPlatformAPI(ctx context.Context, api string, params map[string]any) (json.RawMessage, error) {
	if a.client == nil {
		return nil, fmt.Errorf("telegram api client is not configured")
	}
	return a.client.callRaw(ctx, strings.TrimSpace(api), params)
}

func (a *Adapter) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	outTarget := target
	if outTarget.Empty() {
		return a.SendChat(ctx, outputs)
	}
	return a.sendTarget(ctx, outTarget, outputs)
}

func (a *Adapter) sendContextOutput(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	t, err := contextTarget(ctx)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
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
				logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
				if err != nil {
					logResult.ResultStatus = globalevents.ResultFailed
					logResult.Content = err.Error()
					if errors.Is(err, context.Canceled) {
						logResult.ResultStatus = globalevents.ResultCanceled
					}
				}
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
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
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
		return a.sendText(ctx, t, out.Text, 0, shouldAttachRiskKeyboard(out.Text))
	case delivery.KindReply:
		replyID, _ := strconv.ParseInt(strings.TrimSpace(out.ReplyToPlatformMessageID), 10, 64)
		return a.sendText(ctx, t, out.Text, replyID, shouldAttachRiskKeyboard(out.Text))
	case delivery.KindEmoticon:
		return a.sendSticker(ctx, t, out, 0)
	case delivery.KindImage:
		return a.sendPhoto(ctx, t, out, 0)
	case delivery.KindFile:
		return a.sendDocument(ctx, t, out, 0)
	default:
		return a.sendText(ctx, t, delivery.FallbackText(out), 0, false)
	}
}

func (a *Adapter) sendText(ctx context.Context, t target, text string, replyTo int64, keyboard bool) (delivery.Receipt, globalevents.LogResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}
	switch a.cfg.format() {
	case "rich":
		receipt, logResult, err := a.sendRichText(ctx, t, text, replyTo, keyboard)
		if err == nil || len(receipt.PlatformMessageIDs) > 0 {
			return receipt, logResult, err
		}
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelWarn,
				EventName:    "telegram_rich_message_failed_fallback_to_html",
				Module:       "telegram",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("telegram rich message failed, fallback to html\nerror=%v", err),
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
		return a.sendHTMLText(ctx, t, text, replyTo, keyboard)
	case "plain":
		return a.sendPlainText(ctx, t, text, replyTo, keyboard)
	default:
		return a.sendHTMLText(ctx, t, text, replyTo, keyboard)
	}
}

func (a *Adapter) sendRichText(ctx context.Context, t target, text string, replyTo int64, keyboard bool) (delivery.Receipt, globalevents.LogResult, error) {
	pages := telegramRichTextPages(text)
	var receipt delivery.Receipt
	for i, page := range pages {
		req := sendRichMessageRequest{ChatID: t.ChatID, RichMessage: inputRichMessage{Markdown: page}}
		if i == 0 {
			if replyTo != 0 {
				req.ReplyParameters = &replyParameters{MessageID: replyTo}
			}
			if keyboard {
				req.ReplyMarkup = riskKeyboard()
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

func (a *Adapter) sendHTMLText(ctx context.Context, t target, text string, replyTo int64, keyboard bool) (delivery.Receipt, globalevents.LogResult, error) {
	receipt, logResult, err := a.sendFormattedText(ctx, t, telegramHTMLFromMarkdown(text), "HTML", replyTo, keyboard)
	if err == nil || len(receipt.PlatformMessageIDs) > 0 {
		return receipt, logResult, err
	}
	{
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelWarn,
			EventName:    "telegram_html_message_failed_fallback_to_plain",
			Module:       "telegram",
			Type:         globalevents.ContentText,
			Content:      fmt.Sprintf("telegram html message failed, fallback to plain\nerror=%v", err),
			ContentLimit: 4096,
			ResultStatus: globalevents.ResultFailed,
		}
		if logResult.ResultStatus == globalevents.ResultCanceled {
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
	return a.sendPlainText(ctx, t, text, replyTo, keyboard)
}

func (a *Adapter) sendPlainText(ctx context.Context, t target, text string, replyTo int64, keyboard bool) (delivery.Receipt, globalevents.LogResult, error) {
	return a.sendFormattedText(ctx, t, plainTextFromMarkdown(text), "", replyTo, keyboard)
}

func (a *Adapter) sendFormattedText(ctx context.Context, t target, text, parseMode string, replyTo int64, keyboard bool) (delivery.Receipt, globalevents.LogResult, error) {
	pages := telegramTextPages(text)
	var receipt delivery.Receipt
	for i, page := range pages {
		req := sendMessageRequest{ChatID: t.ChatID, Text: page, ParseMode: parseMode}
		if i == 0 {
			req.ReplyToMessageID = replyTo
			if keyboard {
				req.ReplyMarkup = riskKeyboard()
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
		if logResult.ResultStatus == globalevents.ResultCanceled {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
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

func (a *Adapter) syncBotCommands(ctx context.Context) error {
	commands := telegramBotCommands(a.commandCatalog)
	if len(commands) == 0 {
		return nil
	}
	return a.client.setMyCommands(ctx, commands)
}

func telegramBotCommands(infos []command.Info) []botCommand {
	out := make([]botCommand, 0, len(infos))
	seen := map[string]bool{}
	for _, info := range infos {
		name := strings.TrimSpace(strings.TrimPrefix(info.Name, "/"))
		if !validTelegramCommandName(name) || seen[name] {
			continue
		}
		description := strings.TrimSpace(info.Description)
		if description == "" {
			description = strings.TrimSpace(info.Usage)
		}
		if description == "" {
			continue
		}
		out = append(out, botCommand{Command: name, Description: truncateRunes(description, 256)})
		seen[name] = true
	}
	return out
}

func validTelegramCommandName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		if r == '_' || unicode.IsDigit(r) || ('a' <= r && r <= 'z') {
			continue
		}
		return false
	}
	return true
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
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
	if pageSize <= 0 {
		pageSize = limit
	}
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
