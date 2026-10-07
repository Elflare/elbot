// Package notification delivers runtime notices without owning conversation execution.
package notification

import (
	"context"
	"log/slog"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/session"
)

// Intent preserves the event's original source and activation across asynchronous work.
// PlatformData follows contextinfo's immutable snapshot convention.
type Intent struct {
	Conversation *contextinfo.Conversation
	Binding      *session.Binding
	Notice       delivery.Notice
	sender       delivery.ContextSender
}

type Manager struct {
	sender delivery.MessageSender

	logWithoutSource bool
}

func New(sender delivery.MessageSender, logWithoutSource bool) *Manager {
	return &Manager{sender: sender, logWithoutSource: logWithoutSource}
}

func Capture(ctx context.Context, notice delivery.Notice) Intent {
	intent := Intent{Notice: notice}
	if info, ok := contextinfo.ConversationFromContext(ctx); ok {
		intent.Conversation = &info
	}
	intent.Binding, _ = session.BindingFromContext(ctx)
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		intent.sender = msg.Sender
	}
	return intent
}

func (m *Manager) SendNotice(ctx context.Context, notice delivery.Notice) (delivery.Receipt, error) {
	return m.Send(ctx, Capture(ctx, notice))
}

func (m *Manager) Send(ctx context.Context, intent Intent) (delivery.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return delivery.Receipt{}, err
	}
	if intent.Binding != nil {
		if !intent.Binding.Valid() {
			return delivery.Receipt{}, context.Canceled
		}
		ctx = session.WithBinding(ctx, intent.Binding)
	}
	// Replace routing values, retaining the worker's cancellation. In particular,
	// never borrow another message's Sender or lose a captured discard sender.
	message := platform.MessageContext{Sender: intent.sender}
	if intent.Conversation != nil {
		message.Conversation = *intent.Conversation
	}
	ctx = platform.WithMessageContext(ctx, message)
	if m.logWithoutSource && intent.Notice.Target.Empty() && (intent.Conversation == nil || intent.Conversation.Source.Platform == "") {
		if err := delivery.ValidateOutputs(intent.Notice.Outputs); err != nil {
			return delivery.Receipt{}, err
		}
		target, err := delivery.ValidateOutputsTarget(intent.Notice.Outputs)
		if err != nil {
			return delivery.Receipt{}, err
		}
		if target.Empty() {

			for _, output := range intent.Notice.Outputs {
				_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
					Category: globalevents.LogRuntime,
					Level:    intent.Notice.Level,
					Name:     "runtime_notification",
					Module:   "notification",
					Summary:  "runtime notification" + ": " + output.Text,
					Fields:   []slog.Attr{slog.Any("kind", output.Kind)},
					Detail:   output.Text,
				})
			}

			return delivery.Receipt{}, nil
		}
	}
	return m.sender.SendNotice(ctx, intent.Notice)
}

func (m *Manager) Text(ctx context.Context, level slog.Level, text string) {
	if text = strings.TrimSpace(text); text != "" {
		_, _ = m.SendNotice(ctx, delivery.Notice{Outputs: []delivery.Output{delivery.Text(text)}, Level: level})
	}
}
