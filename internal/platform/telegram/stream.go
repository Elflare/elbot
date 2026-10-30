// stream.go 串行处理流式预览、更新与最终回执，完整操作参与最终关闭。

package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
)

type messageStream struct {
	adapter *Adapter
	target  target

	gate     chan struct{} // 串行操作资格，等待可被调用方或最终关闭取消。
	message  int64
	draftID  int64
	useDraft bool
	text     string
	lastEdit time.Time
	finished bool
}

func (a *Adapter) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	ctx, finish, err := a.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	t, err := contextTarget(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram stream target: %w", err)
	}
	return &messageStream{gate: make(chan struct{}, 1), adapter: a, target: t, draftID: time.Now().UnixNano(), useDraft: strings.HasPrefix(t.ScopeID, "private:") && a.cfg.richMessageEnabled()}, nil
}

func (s *messageStream) Append(ctx context.Context, text string) error {
	ctx, finish, err := s.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if s.finished || text == "" {
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
			return err
		}
		s.lastEdit = time.Now()
		return nil
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
	ctx, finish, err := s.beginOperation(ctx)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	defer finish()
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
			_, _ = s.adapter.client.editMessageText(ctx, editMessageTextRequest{ChatID: s.target.ChatID, MessageID: s.message, Text: "done"})
		}
		receipt, logResult, err := s.adapter.sendRichText(ctx, s.target, text, 0, nil)
		s.finished = true
		return telegramReceipt(receipt, s.target), logResult, err
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
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return delivery.Receipt{}, logResult, err
		}
	}
	receipt := delivery.Receipt{PlatformMessageIDs: []string{formatMessageID(s.message)}}
	for _, page := range pages[1:] {
		pageText, pageParseMode := s.adapter.streamPreviewPayload(page)
		sent, err := s.adapter.client.sendMessage(ctx, sendMessageRequest{ChatID: s.target.ChatID, Text: pageText, ParseMode: pageParseMode})
		if err != nil {
			s.finished = true
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return telegramReceipt(receipt, s.target), logResult, err
		}
		if sent.MessageID != 0 {
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, formatMessageID(sent.MessageID))
		}
	}
	s.finished = true
	return telegramReceipt(receipt, s.target), globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (s *messageStream) Finish(ctx context.Context) (delivery.Receipt, globalevents.LogResult, error) {
	ctx, finish, err := s.beginOperation(ctx)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return delivery.Receipt{}, logResult, err
	}
	defer finish()
	s.finished = true
	if s.message == 0 {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}
	return telegramReceipt(delivery.Receipt{PlatformMessageIDs: []string{formatMessageID(s.message)}}, s.target), globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (a *Adapter) streamPreviewPayload(text string) (string, string) {
	switch a.cfg.Format {
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

func (s *messageStream) beginOperation(ctx context.Context) (context.Context, func(), error) {
	ctx, finish, err := s.adapter.beginOperation(ctx)
	if err != nil {
		return nil, nil, err
	}
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		finish()
		return nil, nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-s.gate
		finish()
		return nil, nil, err
	}
	return ctx, func() { <-s.gate; finish() }, nil
}
