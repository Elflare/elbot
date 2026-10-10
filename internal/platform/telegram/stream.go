package telegram

import (
	"context"
	"elbot/internal/contextinfo"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
)

type messageStream struct {
	adapter *Adapter
	target  target

	mu       sync.Mutex
	message  int64
	draftID  int64
	useDraft bool
	text     string
	lastEdit time.Time
	finished bool
}

func (a *Adapter) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	t, err := contextTarget(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram stream target: %w", err)
	}
	return &messageStream{adapter: a, target: t, draftID: time.Now().UnixNano(), useDraft: strings.HasPrefix(t.ScopeID, "private:") && a.cfg.richMessageEnabled()}, nil
}

func (s *messageStream) Append(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return nil
	}
	s.text += text
	preview := streamPreviewText(s.text)
	previewText, previewParseMode := s.adapter.streamPreviewPayload(preview)
	if strings.TrimSpace(previewText) == "" {
		return nil
	}
	if s.useDraft {
		if time.Since(s.lastEdit) < s.adapter.cfg.streamEditInterval() {
			return nil
		}
		if err := s.adapter.client.sendRichMessageDraft(ctx, sendRichMessageDraftRequest{ChatID: s.target.ChatID, DraftID: s.draftID, RichMessage: inputRichMessage{Markdown: preview}}); err != nil {
			s.useDraft = false
		} else {
			s.lastEdit = time.Now()
			return nil
		}
	}
	if s.message == 0 {
		sent, err := s.adapter.client.sendMessage(ctx, sendMessageRequest{ChatID: s.target.ChatID, Text: previewText, ParseMode: previewParseMode})
		if err != nil {
			return err
		}
		s.message = sent.MessageID
		s.lastEdit = time.Now()
		return nil
	}
	if time.Since(s.lastEdit) < s.adapter.cfg.streamEditInterval() {
		return nil
	}
	if _, err := s.adapter.client.editMessageText(ctx, editMessageTextRequest{ChatID: s.target.ChatID, MessageID: s.message, Text: previewText, ParseMode: previewParseMode}); err != nil {
		return err
	}
	s.lastEdit = time.Now()
	return nil
}

func (s *messageStream) Replace(ctx context.Context, text string) (delivery.Receipt, globalevents.LogResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		text = strings.TrimSpace(s.text)
	}
	if text == "" {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}
	if s.adapter.cfg.richMessageEnabled() {
		if s.message != 0 {
			_, _ = s.adapter.client.editMessageText(ctx, editMessageTextRequest{ChatID: s.target.ChatID, MessageID: s.message, Text: "回复已生成："})
		}
		receipt, logResult, err := s.adapter.sendRichText(ctx, s.target, text, 0, false)
		if err == nil || len(receipt.PlatformMessageIDs) > 0 {
			s.finished = true
			return telegramReceipt(receipt, s.target), logResult, err
		}
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelWarn,
				EventName:    "telegram_rich_stream_final_failed_fallback_to_sendmessage",
				Module:       "telegram",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("telegram rich stream final failed, fallback to sendMessage\nerror=%v", err),
				ContentLimit: 4096,
				ResultStatus: logResult.ResultStatus,
			}
			if logResult.ResultStatus == globalevents.ResultCanceled {
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
	pages := telegramTextPages(text)
	if s.message == 0 {
		pageText, pageParseMode := s.adapter.streamPreviewPayload(pages[0])
		sent, err := s.adapter.client.sendMessage(ctx, sendMessageRequest{ChatID: s.target.ChatID, Text: pageText, ParseMode: pageParseMode})
		if err != nil {
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return delivery.Receipt{}, logResult, err
		}
		s.message = sent.MessageID
	} else {
		pageText, pageParseMode := s.adapter.streamPreviewPayload(pages[0])
		if _, err := s.adapter.client.editMessageText(ctx, editMessageTextRequest{ChatID: s.target.ChatID, MessageID: s.message, Text: pageText, ParseMode: pageParseMode}); err != nil {
			{
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
	}
	receipt := delivery.Receipt{PlatformMessageIDs: []string{formatMessageID(s.message)}}
	for _, page := range pages[1:] {
		pageText, pageParseMode := s.adapter.streamPreviewPayload(page)
		sent, err := s.adapter.client.sendMessage(ctx, sendMessageRequest{ChatID: s.target.ChatID, Text: pageText, ParseMode: pageParseMode})
		if err != nil {
			s.finished = true
			{
				logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
				if err != nil {
					logResult.ResultStatus = globalevents.ResultFailed
					logResult.Content = err.Error()
					if errors.Is(err, context.Canceled) {
						logResult.ResultStatus = globalevents.ResultCanceled
					}
				}
				return telegramReceipt(receipt, s.target), logResult, err
			}
		}
		if sent.MessageID != 0 {
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, formatMessageID(sent.MessageID))
		}
	}
	s.finished = true
	return telegramReceipt(receipt, s.target), globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (s *messageStream) Finish(ctx context.Context) (delivery.Receipt, globalevents.LogResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = true
	if s.message == 0 {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}
	return telegramReceipt(delivery.Receipt{PlatformMessageIDs: []string{formatMessageID(s.message)}}, s.target), globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (a *Adapter) streamPreviewPayload(text string) (string, string) {
	switch a.cfg.format() {
	case "plain", "rich":
		return plainTextFromMarkdown(text), ""
	default:
		return telegramHTMLFromMarkdown(text), "HTML"
	}
}

func streamPreviewText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= telegramTextPageRunes {
		return text
	}
	return string(runes[:telegramTextPageRunes-24]) + "\n……（生成中，完整内容稍后发送）"
}
