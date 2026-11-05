// stream.go 串行处理流式预览、更新与最终回执，完整操作参与最终关闭。

package telegram

import (
	"context"
	"elbot/internal/contextinfo"
	"fmt"
	"strings"
	"time"

	"elbot/internal/delivery"
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
	t, err := a.contextTarget(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram stream target: %w", err)
	}
	return &messageStream{gate: make(chan struct{}, 1), adapter: a, target: t, draftID: time.Now().UnixNano(), useDraft: t.ChatType == contextinfo.ChatPrivate && a.cfg.richMessageEnabled()}, nil
}

func (s *messageStream) Send(ctx context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	ctx, finish, err := s.beginOperation(ctx)
	if err != nil {
		return delivery.Receipt{}, err
	}
	defer finish()
	switch event.Type {
	case delivery.StreamTextDelta:
		return delivery.Receipt{}, s.sendPreview(ctx, event.Text)
	case delivery.StreamReset:
		visible := !s.lastEdit.IsZero()
		s.text, s.lastEdit = "", time.Time{}
		if s.useDraft && visible {
			return delivery.Receipt{}, s.adapter.client.sendRichMessageDraft(ctx, sendRichMessageDraftRequest{
				ChatID: s.target.NativeChatID, DraftID: s.draftID,
				RichMessage: inputRichMessage{HTML: "<tg-thinking>重新生成中…</tg-thinking>"},
			})
		}
		if s.message != 0 {
			_, err := s.adapter.client.editMessageText(ctx, editMessageTextRequest{ChatID: s.target.NativeChatID, MessageID: s.message, Text: "重新生成中…"})
			return delivery.Receipt{}, err
		}
	case delivery.StreamSend:
		if event.Outputs != nil {
			return s.adapter.SendTo(ctx, delivery.Target{}, event.Outputs)
		}
		return s.sendFinal(ctx, event.Text, event.Actions)
	}
	// Finish and reasoning events do not submit a message or release its preview.
	return delivery.Receipt{}, nil
}

func (s *messageStream) sendPreview(ctx context.Context, text string) error {
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
		if err := s.adapter.client.sendRichMessageDraft(ctx, sendRichMessageDraftRequest{ChatID: s.target.NativeChatID, DraftID: s.draftID, RichMessage: inputRichMessage{Markdown: preview}}); err != nil {
			return err
		}
		s.lastEdit = time.Now()
		return nil
	}
	if s.message == 0 {
		sent, err := s.adapter.client.sendMessage(ctx, sendMessageRequest{ChatID: s.target.NativeChatID, Text: previewText, ParseMode: previewParseMode})
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
	if _, err := s.adapter.client.editMessageText(ctx, editMessageTextRequest{ChatID: s.target.NativeChatID, MessageID: s.message, Text: previewText, ParseMode: previewParseMode}); err != nil {
		return err
	}
	s.lastEdit = time.Now()
	return nil
}

func (s *messageStream) sendFinal(ctx context.Context, text string, actions []delivery.Action) (delivery.Receipt, error) {
	if s.finished {
		return delivery.Receipt{}, nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return delivery.Receipt{}, nil
	}
	pages := telegramTextPages(text)
	if s.adapter.cfg.richMessageEnabled() {
		pages = telegramRichTextPages(text)
	}
	var receipt delivery.Receipt
	for i, page := range pages {
		pageText, pageParseMode := s.adapter.streamPreviewPayload(page)
		req := editMessageTextRequest{ChatID: s.target.NativeChatID, Text: pageText, ParseMode: pageParseMode}
		if s.adapter.cfg.richMessageEnabled() {
			req.Text, req.ParseMode = "", ""
			req.RichMessage = &inputRichMessage{Markdown: page}
		}
		if i == 0 {
			req.MessageID, req.ReplyMarkup = s.message, commandKeyboard(actions)
		}
		var sent sentMessage
		var err error
		switch {
		case req.MessageID != 0:
			_, err = s.adapter.client.editMessageText(ctx, req)
			sent.MessageID = req.MessageID
		case req.RichMessage != nil:
			sent, err = s.adapter.client.sendRichMessage(ctx, sendRichMessageRequest{ChatID: req.ChatID, RichMessage: *req.RichMessage, ReplyMarkup: req.ReplyMarkup})
		default:
			sent, err = s.adapter.client.sendMessage(ctx, sendMessageRequest{ChatID: req.ChatID, Text: req.Text, ParseMode: req.ParseMode, ReplyMarkup: req.ReplyMarkup})
		}
		if err != nil {
			s.finished = true
			return s.adapter.telegramReceipt(receipt, s.target), err
		}
		if i == 0 {
			s.message = sent.MessageID
		}
		if sent.MessageID != 0 {
			receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, formatMessageID(sent.MessageID))
		}
	}
	s.finished = true
	return s.adapter.telegramReceipt(receipt, s.target), nil
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
