package execution

import (
	"context"
	"log/slog"
	"testing"

	agentinput "elbot/internal/agent/input"

	agentshare "elbot/internal/agent/share"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"

	"elbot/internal/security"
)

type constructorHooks struct{}

func (constructorHooks) Run(_ context.Context, e hook.Event) (hook.Event, events.LogResult, error) {
	return e, events.LogResult{ResultStatus: events.ResultSkipped}, nil
}

func (constructorHooks) Notify(context.Context, hook.Event) {}

func (constructorHooks) FillContext(_ context.Context, e hook.Event) hook.Event { return e }

func (constructorHooks) NotifyError(context.Context, hook.Event, error) {}

func (constructorHooks) CancelRoute(hook.Event) bool { return false }

func (constructorHooks) Route(_ context.Context, e hook.Event) (hook.Event, bool, error) {
	return e, false, nil
}

func (constructorHooks) HasRoute(hook.Event) bool { return false }

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
	entry := agentinput.NewMessageHandler(agentinput.MessageHandlerDeps{Identity: identity, Hooks: constructorHooks{}, Output: constructorOutput{}, Commands: constructorCommands{}, Input: input})
	if err := entry.HandleMessage(t.Context(), "plain input"); err != nil {
		t.Fatal(err)
	}
	if input.text != "plain input" || !input.expired {
		t.Fatalf("input=%+v", input)
	}
}
