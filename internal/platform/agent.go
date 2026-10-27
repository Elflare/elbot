// agent.go 连接 Agent 公开能力，转换消息输入和订阅事件。

package platform

import (
	"context"
	"errors"
	"slices"

	"elbot/internal/agent"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/platform/cli"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
)

// ConnectAgent 在启动前一次性完成接线，子包只获得其实际使用的能力。
func (m *Supervisor) ConnectAgent(source AgentCapabilities) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return signal.ErrClosed
	}
	if m.started {
		return errors.New("Agent must connect before platforms start")
	}
	if m.agentConnected {
		return m.agentErr
	}
	m.agentConnected, m.agent = true, source
	defer m.configureCloseSteps()
	var needsUI bool
	for _, adapter := range m.adapters {
		if target, ok := adapter.(interface {
			SetCompleter(func(context.Context, completion.Request) ([]completion.Item, error))
		}); ok {
			target.SetCompleter(source.Complete)
		}
		if target, ok := adapter.(interface{ SetCommandCatalog([]command.Info) }); ok {
			target.SetCommandCatalog(source.Commands())
		}
		switch adapter.(type) {
		case *cli.Adapter, *cli.RemoteServer:
			needsUI = true
		}
	}
	if needsUI {
		m.ui, m.agentErr = cli.ConnectUI(agentEvents{source})
	}
	return m.agentErr
}

func (m *Supervisor) handleMessage(ctx context.Context, text string, msg share.MessageContext) error {
	ctx = share.WithMessageContext(ctx, msg)
	// Hook 仍通过根部读取原始平台事实；发送器和协议对象不进入 Agent 请求。
	ctx = WithMessageContext(ctx, projectMessageContext(msg))
	return m.agent.HandleMessage(ctx, messageRequest(text, msg))
}

func messageRequest(text string, msg share.MessageContext) agent.MessageRequest {
	request := agent.MessageRequest{
		Text: text, RawText: msg.RawText, GroupRole: msg.GroupRole,
		BufferAssistantOutput: msg.BufferAssistantOutput,
		ForkFromMessageID:     msg.ForkFromMessageID, ResumeSessionID: msg.ResumeSessionID,
		Segments: messageSegments(msg.Segments), ContextText: msg.ContextText,
		ContextSegments: messageSegments(msg.ContextSegments),
		Reply:           agent.ReplyContext{MessageID: msg.Reply.MessageID, SenderID: msg.Reply.SenderID, SenderName: msg.Reply.SenderName, Text: msg.Reply.Text, Segments: messageSegments(msg.Reply.Segments)},
		Bot:             agent.Identity{UserID: msg.Bot.UserID, Username: msg.Bot.Username},
		TriggerKeywords: slices.Clone(msg.TriggerKeywords),
	}
	for _, mention := range msg.Mentions {
		request.Mentions = append(request.Mentions, agent.Mention{UserID: mention.UserID, Username: mention.Username, Text: mention.Text})
	}
	if resolver := msg.MediaResolver; resolver != nil {
		request.ResolveMedia = func(ctx context.Context, input media.PlatformMedia, limit int64) (delivery.Source, error) {
			return resolver.ResolveMedia(ctx, share.MessageSegment{Type: share.MessageSegmentType(input.Type), MediaID: input.MediaID, URL: input.URL, PlatformFileID: input.PlatformFileID, MIMEType: input.MIMEType, Name: input.Name, Size: input.Size}, limit)
		}
	}
	return request
}

func messageSegments(segments []share.MessageSegment) []agent.MessageSegment {
	if segments == nil {
		return nil
	}
	result := make([]agent.MessageSegment, len(segments))
	for i, segment := range segments {
		result[i] = agent.MessageSegment{Type: string(segment.Type), Text: segment.Text, UserID: segment.UserID,
			Media: media.PlatformMedia{Type: string(segment.Type), MediaID: segment.MediaID, URL: segment.URL, PlatformFileID: segment.PlatformFileID, MIMEType: segment.MIMEType, Name: segment.Name, Size: segment.Size}}
	}
	return result
}

func projectMessageContext(msg share.MessageContext) MessageContext {
	segments := func(input []share.MessageSegment) []MessageSegment {
		if input == nil {
			return nil
		}
		result := make([]MessageSegment, len(input))
		for i, segment := range input {
			result[i] = MessageSegment{Type: MessageSegmentType(segment.Type), Text: segment.Text, UserID: segment.UserID, URL: segment.URL, MediaID: segment.MediaID, PlatformFileID: segment.PlatformFileID, MIMEType: segment.MIMEType, Name: segment.Name, Size: segment.Size}
		}
		return result
	}
	return MessageContext{
		RawText: msg.RawText, PlatformMessage: slices.Clone(msg.PlatformMessage),
		Reply: ReplyContext{MessageID: msg.Reply.MessageID, SenderID: msg.Reply.SenderID, SenderName: msg.Reply.SenderName, Text: msg.Reply.Text, Segments: segments(msg.Reply.Segments)},
	}
}

// agentEvents 将 Agent 公共事件转为 CLI 自有展示数据，不泄露完整 Agent。
type agentEvents struct{ source AgentCapabilities }

func (e agentEvents) ObserveStatusChanged(receive func(cli.StatusChangedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	return e.source.ObserveStatusChanged(func(event agent.StatusChangedEvent) error {
		return receive(cli.StatusChangedEvent{Snapshot: event.Snapshot, Version: event.Version, Execution: event.Execution, Conversation: event.Conversation, Binding: bindingSnapshot(event.Binding)})
	}, options)
}

func (e agentEvents) ObserveBindingChanged(receive func(cli.BindingChangedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	return e.source.ObserveBindingChanged(func(event agent.BindingChangedEvent) error {
		return receive(cli.BindingChangedEvent{Version: event.Version, Old: bindingSnapshot(event.Old), New: bindingSnapshot(event.New)})
	}, options)
}

func bindingSnapshot(binding *agent.BindingSnapshot) *cli.BindingSnapshot {
	if binding == nil {
		return nil
	}
	return &cli.BindingSnapshot{Scope: binding.Scope, SessionID: binding.SessionID, Version: binding.Version}
}
