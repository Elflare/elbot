// Package notification delivers runtime notices without owning conversation execution.
package notification

import (
	"context"
	"log/slog"
	"strings"

	"elbot/internal/chatinfo"
	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/session"
)

// Intent preserves the event's original source and activation across asynchronous work.
// PlatformData follows chatinfo's immutable snapshot convention.
type Intent struct {
	Info    *chatinfo.Info
	Binding *session.Binding
	Notice  delivery.Notice
	sender  delivery.ContextSender
}

type Manager struct {
	sender           delivery.MessageSender
	logger           *slog.Logger
	logWithoutSource bool
}

func New(sender delivery.MessageSender, logger *slog.Logger, logWithoutSource bool) *Manager {
	return &Manager{sender: sender, logger: logger, logWithoutSource: logWithoutSource}
}

func Capture(ctx context.Context, notice delivery.Notice) Intent {
	intent := Intent{Notice: notice}
	if info, ok := chatinfo.FromContext(ctx); ok {
		intent.Info = &info
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
	if intent.Info != nil {
		message.Info = *intent.Info
	}
	ctx = platform.WithMessageContext(ctx, message)
	if m.logWithoutSource && intent.Notice.Target.Empty() && (intent.Info == nil || intent.Info.Source.Platform == "") {
		if err := delivery.ValidateOutputs(intent.Notice.Outputs); err != nil {
			return delivery.Receipt{}, err
		}
		target, err := delivery.ValidateOutputsTarget(intent.Notice.Outputs)
		if err != nil {
			return delivery.Receipt{}, err
		}
		if target.Empty() {
			if m.logger != nil {
				for _, output := range intent.Notice.Outputs {
					m.logger.Log(ctx, intent.Notice.Level, "runtime notification", "text", output.Text, "kind", output.Kind)
				}
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
