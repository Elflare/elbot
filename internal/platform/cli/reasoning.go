// reasoning.go 处理思考文本流的 CLI 展示与收尾。

package cli

import (
	"context"
	"time"

	"elbot/internal/delivery"
)

// reasoningStream is used sequentially by one model call, without a signal queue.
type reasoningStream struct {
	ui       *UI
	target   messageData
	ctx      context.Context
	finished bool
	failed   bool
}

func (u *UI) StartReasoning(context.Context) delivery.ReasoningStream {
	return &reasoningStream{ui: u}
}

func (s *reasoningStream) Append(ctx context.Context, text string) {
	if text == "" || s.finished || ctx.Err() != nil || s.ui.display.ctx.Err() != nil {
		return
	}
	target := uiTarget(ctx)
	if target == (messageData{}) {
		return
	}
	if target != s.target {
		s.closeTarget()
		s.target, s.ctx = target, ctx
		s.failed = false
		text = "[thinking] " + text
	}
	if s.failed {
		return
	}
	if err := target.sendReasoning(ctx, text); err != nil {
		s.failed = true
	}
}

func (s *reasoningStream) Finish(context.Context) {
	if s.finished {
		return
	}
	s.finished = true
	s.closeTarget()
}

func (s *reasoningStream) closeTarget() {
	if s.target == (messageData{}) {
		return
	}
	if s.ui.display.ctx.Err() == nil {
		// A call ending is a presentation boundary even when its request canceled.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 30*time.Second)
		stop := context.AfterFunc(s.ui.display.ctx, cancel)
		_ = s.target.sendReasoning(ctx, "[/thinking]\n\n")
		stop()
		cancel()
	}
	s.target = messageData{}
}

func (d messageData) sendReasoning(ctx context.Context, text string) error {
	if d.client != nil {
		_, err := d.client.write(ctx, remoteMessage{Type: remoteMsgReasoning, Text: text})
		return err
	}
	return d.adapter.sendTUIMessage(tuiReasoningMsg(text), text)
}
