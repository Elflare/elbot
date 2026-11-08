// send.go 提供对外消息发送、正文流与思考流入口。

package platform

import (
	"context"
	"errors"

	"elbot/internal/command"
	"elbot/internal/delivery"
	"elbot/internal/platform/share"
)

func (m *Supervisor) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	return m.dispatcher.SendTo(ctx, target, outputs)
}

func (m *Supervisor) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	return m.dispatcher.StartStream(ctx)
}

// SendCommandResult 交付一次命令结果；结构化协议由当前输入的回复目标提供。
func (m *Supervisor) SendCommandResult(ctx context.Context, req command.Request, result *command.Result, failure *command.Error) (delivery.Receipt, error) {
	if msg, ok := share.MessageContextFrom(ctx); ok && msg.CommandReply != nil {
		return msg.CommandReply.SendCommandResult(ctx, req, result, failure)
	}
	return delivery.Receipt{}, errors.New("command reply target missing")
}
