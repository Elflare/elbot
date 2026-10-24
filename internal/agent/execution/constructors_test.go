package execution

import (
	"context"
	"log/slog"
	"testing"

	agentinput "elbot/internal/agent/input"

	agentshare "elbot/internal/agent/share"

	agenthooks "elbot/internal/agent/hooks"
	"elbot/internal/delivery"
	"elbot/internal/events"

	"elbot/internal/security"
)

type constructorMessageHooks struct{}

func (constructorMessageHooks) HasRoute(context.Context, agenthooks.Message) bool    { return false }
func (constructorMessageHooks) CancelRoute(context.Context, agenthooks.Message) bool { return false }
func (constructorMessageHooks) PlatformMessageReceived(_ context.Context, message agenthooks.Message) (agenthooks.PlatformMessageReceivedResult, events.LogResult, error) {
	return agenthooks.PlatformMessageReceivedResult{Segments: message.Segments}, events.LogResult{}, nil
}

type constructorCommands struct{}

func (constructorCommands) Handle(context.Context, string) (bool, error) { return false, nil }

func (constructorCommands) IsCommand(string) bool { return false }

type constructorInput struct {
	text    string
	expired bool
}

func (i *constructorInput) HandleInput(_ context.Context, text string) error {
	i.text = text
	return nil
}

func (i *constructorInput) ExpireIdleCurrentSession(context.Context) error {
	i.expired = true
	return nil
}

type constructorOutput struct{}

func (constructorOutput) Notify(context.Context, slog.Level, string) {}

func (constructorOutput) SendChat(context.Context, string) error { return nil }

func (constructorOutput) SendOutputs(context.Context, []delivery.Output) (events.LogResult, error) {
	return events.LogResult{ResultStatus: events.ResultSkipped}, nil
}

func TestMessageEntryConstructsWithOnlyConsumptionCapabilities(t *testing.T) {
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Permission: security.DefaultPermission()})
	input := &constructorInput{}
	entry := agentinput.NewMessageHandler(agentinput.MessageHandlerDeps{Identity: identity, Hooks: constructorMessageHooks{}, Output: constructorOutput{}, Commands: constructorCommands{}, Input: input})
	if err := entry.HandleMessage(t.Context(), "plain input"); err != nil {
		t.Fatal(err)
	}
	if input.text != "plain input" || !input.expired {
		t.Fatalf("input=%+v", input)
	}
}
