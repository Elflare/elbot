package cli

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/signal"
)

type testAgentEvents struct {
	status  *signal.Signal[StatusChangedEvent, signal.Empty]
	binding *signal.Signal[BindingChangedEvent, signal.Empty]
}

func (s testAgentEvents) ObserveStatusChanged(receive func(StatusChangedEvent), options signal.ConnectOptions) (*signal.Connection, error) {
	return s.status.Observe(func(_ context.Context, event StatusChangedEvent) { receive(event) }, options)
}
func (s testAgentEvents) ObserveBindingChanged(receive func(BindingChangedEvent), options signal.ConnectOptions) (*signal.Connection, error) {
	return s.binding.Observe(func(_ context.Context, event BindingChangedEvent) { receive(event) }, options)
}
func testConversation(ctx context.Context) *contextinfo.Conversation {
	info, ok := contextinfo.ConversationFromContext(ctx)
	if !ok {
		return nil
	}
	return &info
}
