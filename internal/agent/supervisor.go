package agent

import (
	"context"
	agentexecution "elbot/internal/agent/execution"
	agenthooks "elbot/internal/agent/hooks"
	agentinput "elbot/internal/agent/input"
	"elbot/internal/agent/routes"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/contextmgr"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/turn"
	"sync"
)

// Supervisor owns foreground and background execution and its independently managed Session, Request and Turn state.
type Supervisor struct {
	signals               signals // Owned event sources; closed after internal producers finish.
	ctx                   context.Context
	cancel                context.CancelFunc
	sessions              *session.Supervisor
	requests              *request.Manager
	turns                 *turn.Supervisor
	routes                *routes.Registry
	contexts              *contextmgr.Service
	commands              *command.Router
	background            *agentinput.BackgroundHandler // Prepares task input using owned Session and execution capabilities.
	handler               *agentinput.MessageHandler
	completion            *completion.Service
	execution             *agentexecution.ExecutionCoordinator
	bridge                *agenthooks.HookBridge
	bindings              *signalBindings
	mu                    sync.Mutex
	started, closed       bool
	done                  chan struct{}
	restoreMessageContext func(context.Context, context.Context) context.Context // 注入的平台上下文恢复能力，不读取平台内部值。
}

func (m *Supervisor) HandleMessage(ctx context.Context, req MessageRequest) error {
	msg := agentshare.MessageContext{
		RawText: req.RawText, GroupRole: req.GroupRole, BufferAssistantOutput: req.BufferAssistantOutput,
		ForkFromMessageID: req.ForkFromMessageID, ResumeSessionID: req.ResumeSessionID,
		Segments: messageSegments(req.Segments), ContextText: req.ContextText, ContextSegments: messageSegments(req.ContextSegments),
		Reply: agentshare.ReplyContext{MessageID: req.Reply.MessageID, SenderID: req.Reply.SenderID, SenderName: req.Reply.SenderName, Text: req.Reply.Text, Segments: messageSegments(req.Reply.Segments)},
		Bot:   agentshare.Identity{UserID: req.Bot.UserID, Username: req.Bot.Username}, TriggerKeywords: req.TriggerKeywords,
		ResolveMedia: req.ResolveMedia, RestoreMessageContext: m.restoreMessageContext,
	}
	for _, mention := range req.Mentions {
		msg.Mentions = append(msg.Mentions, agentshare.Mention{UserID: mention.UserID, Username: mention.Username, Text: mention.Text})
	}
	return m.handler.HandleMessage(agentshare.WithMessageContext(ctx, msg), req.Text)
}

func messageSegments(input []MessageSegment) []agentshare.MessageSegment {
	if input == nil {
		return nil
	}
	result := make([]agentshare.MessageSegment, len(input))
	for i, segment := range input {
		result[i] = agentshare.MessageSegment{Type: segment.Type, Text: segment.Text, UserID: segment.UserID, Media: segment.Media}
	}
	return result
}

func (m *Supervisor) Complete(ctx context.Context, req completion.Request) ([]completion.Item, error) {
	return m.completion.Complete(ctx, req)
}

// Commands returns detached metadata, never the mutable command router.
func (m *Supervisor) Commands() []command.Info {
	commands := m.commands.Commands()
	for i := range commands {
		commands[i].Aliases = append([]string(nil), commands[i].Aliases...)
	}
	return commands
}

// ObserveStatusChanged gives each subscriber detached facts without the internal context.
// Handlers update memory or enqueue work; the caller owns its subscription and work lifecycle.
func (m *Supervisor) ObserveStatusChanged(handler func(StatusChangedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, signal.ErrClosed
	}
	return m.signals.StatusChanged.Observe(func(_ context.Context, event agentexecution.StatusChangedEvent) error {
		event.Snapshot.Usage = agentshare.CloneUsage(event.Snapshot.Usage)
		if event.Conversation != nil {
			conversation := *event.Conversation
			event.Conversation = &conversation
		}
		snapshot := StatusChangedEvent{Snapshot: event.Snapshot, Version: event.Version, Execution: event.Execution, Conversation: event.Conversation}
		if event.Binding != nil {
			snapshot.Binding = &BindingSnapshot{Scope: event.Binding.Scope, SessionID: event.Binding.SessionID, Version: event.Binding.Version}
		}
		return handler(snapshot)
	}, options)
}

// ObserveBindingChanged projects internal activation handles into detached facts.
// Handlers update memory or enqueue work; the caller owns its subscription and work lifecycle.
func (m *Supervisor) ObserveBindingChanged(handler func(BindingChangedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, signal.ErrClosed
	}
	return m.sessions.ObserveBindingChanged(func(_ context.Context, event session.BindingChangedEvent) error {
		snapshot := BindingChangedEvent{Version: event.Version}
		if event.Old != nil {
			snapshot.Old = &BindingSnapshot{Scope: event.Old.Scope(), SessionID: event.Old.SessionID(), Version: event.Old.Version()}
		}
		if event.New != nil {
			snapshot.New = &BindingSnapshot{Scope: event.New.Scope(), SessionID: event.New.SessionID(), Version: event.New.Version()}
		}
		return handler(snapshot)
	}, options)
}
