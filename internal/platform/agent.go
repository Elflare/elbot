// agent.go 连接 Agent 公开能力，转换消息输入和订阅事件。

package platform

import (
	"context"
	"elbot/internal/operationresult"
	"errors"
	"slices"
	"strings"

	"elbot/internal/agent"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform/cli"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
	"elbot/internal/workspace"
)

// NormalizeInput applies source-platform input normalization after Session resolution.
func (*Supervisor) NormalizeInput(ctx context.Context, segments []llm.MessageSegment) ([]llm.MessageSegment, error) {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || info.Source.Platform != config.PlatformCLI {
		return segments, nil
	}
	var root string
	for i := range segments {
		if segments[i].Type != llm.SegmentText || !strings.Contains(segments[i].Text, "#") {
			continue
		}
		if root == "" {
			var err error
			root, err = workspace.CurrentWorkspaceDir(ctx)
			if err != nil {
				return nil, err
			}
		}
		var err error
		segments[i].Text, err = workspace.ExpandFileReferences(ctx, root, segments[i].Text)
		if err != nil {
			return nil, err
		}
	}
	return segments, nil
}

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
	for _, connect := range m.agentWiring {
		if m.agentErr = connect(source); m.agentErr != nil {
			break
		}
	}
	return m.agentErr
}

func (m *Supervisor) handleMessage(ctx context.Context, text string, msg share.MessageContext) error {
	ctx = share.WithMessageContext(ctx, msg)
	// Hook 仍通过根部读取原始平台事实；发送器和协议对象不进入 Agent 请求。
	ctx = WithMessageContext(ctx, projectMessageContext(msg))
	return m.agent.HandleMessage(ctx, messageRequest(text, msg))
}

// RestoreMessageContext 恢复平台公开事实及内部回复路由；其他上下文仍取自 ctx。
func (m *Supervisor) RestoreMessageContext(ctx, source context.Context) context.Context {
	ctx = share.RestoreMessageContext(ctx, source)
	if msg, ok := MessageContextFrom(source); ok {
		return WithMessageContext(ctx, msg)
	}
	return WithoutMessageContext(ctx)
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
		request.ResolveMedia = func(ctx context.Context, input media.PlatformMedia, limit int64) (operationresult.Result[delivery.Source], error) {
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
	reply := msg.Reply
	reply.Segments = slices.Clone(reply.Segments)
	return MessageContext{
		RawText: msg.RawText, PlatformMessage: slices.Clone(msg.PlatformMessage),
		Reply: reply,
	}
}

// agentEvents 将 Agent 公共事件转为 CLI 自有展示数据，不泄露完整 Agent。
type agentEvents struct{ source AgentCapabilities }

func (e agentEvents) ObserveStatusChanged(receive func(cli.StatusChangedEvent), options signal.ConnectOptions) (*signal.Connection, error) {
	return e.source.ObserveStatusChanged(func(event agent.StatusChangedEvent) {
		receive(cli.StatusChangedEvent{Snapshot: event.Snapshot, Version: event.Version, Execution: event.Execution, Conversation: event.Conversation, Binding: bindingSnapshot(event.Binding)})
	}, options)
}

func (e agentEvents) ObserveBindingChanged(receive func(cli.BindingChangedEvent), options signal.ConnectOptions) (*signal.Connection, error) {
	return e.source.ObserveBindingChanged(func(event agent.BindingChangedEvent) {
		receive(cli.BindingChangedEvent{Version: event.Version, Old: bindingSnapshot(event.Old), New: bindingSnapshot(event.New)})
	}, options)
}

func bindingSnapshot(binding *agent.BindingSnapshot) *cli.BindingSnapshot {
	if binding == nil {
		return nil
	}
	return &cli.BindingSnapshot{Scope: binding.Scope, SessionID: binding.SessionID, Version: binding.Version}
}
