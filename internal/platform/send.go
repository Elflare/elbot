// send.go 提供对外消息发送、正文流与思考流入口。

package platform

import (
	"context"

	"elbot/internal/delivery"
)

func (m *Supervisor) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	return m.dispatcher.SendTo(ctx, target, outputs)
}

func (m *Supervisor) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	return m.dispatcher.StartStream(ctx)
}

func (m *Supervisor) StartReasoning(ctx context.Context) delivery.ReasoningStream {
	if m.ui == nil {
		return nil
	}
	return m.ui.StartReasoning(ctx)
}
