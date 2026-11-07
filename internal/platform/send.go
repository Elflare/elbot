// send.go 提供对外消息发送、正文流与思考流入口。

package platform

import (
	"context"

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
func (m *Supervisor) SendCommandResult(ctx context.Context, name string, result *command.Result, failure *command.Error) (delivery.Receipt, error) {
	if msg, ok := share.MessageContextFrom(ctx); ok && msg.CommandReply != nil {
		return msg.CommandReply.SendCommandResult(ctx, name, result, failure)
	}
	var outputs []delivery.Output
	if result != nil && (result.Text != "" || result.Confirmation != nil) {
		out := delivery.Text(result.Text)
		if result.Confirmation != nil {
			out.Actions = result.Confirmation.Actions
		}
		outputs = append(outputs, out)
	}
	if failure != nil {
		outputs = append(outputs, delivery.Text(failure.Message))
	}
	return m.SendTo(ctx, delivery.Target{}, outputs)
}
