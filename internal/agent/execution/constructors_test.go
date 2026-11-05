package execution

import (
	"context"
	"log/slog"
	"testing"

	agentinput "elbot/internal/agent/input"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/delivery"

	"elbot/internal/security"
)

type constructorMessageHooks struct{}

func (constructorMessageHooks) HasRoute(context.Context, agentshare.Message) bool    { return false }
func (constructorMessageHooks) CancelRoute(context.Context, agentshare.Message) bool { return false }
func (constructorMessageHooks) PlatformMessageReceived(_ context.Context, message agentshare.Message) (agentshare.PlatformMessageReceivedResult, error) {
	return agentshare.PlatformMessageReceivedResult{Segments: message.Segments}, nil
}

type constructorCommands struct{}

func (constructorCommands) Handle(ctx context.Context, _ string) (context.Context, bool, error) {
	return ctx, false, nil
}

func (constructorCommands) IsCommand(string) bool { return false }

type constructorInput struct {
	text string
}

func (i *constructorInput) HandleInput(ctx context.Context, text string) (context.Context, error) {
	i.text = text
	return ctx, nil
}

type constructorOutput struct{}

func (constructorOutput) Notify(context.Context, slog.Level, string) {}

func (constructorOutput) Send(context.Context, delivery.StreamEvent) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}

func TestMessageEntryConstructsWithOnlyConsumptionCapabilities(t *testing.T) {
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})
	input := &constructorInput{}
	entry := agentinput.NewMessageHandler(agentinput.MessageHandlerDeps{Identity: identity, Hooks: constructorMessageHooks{}, Output: constructorOutput{}, Commands: constructorCommands{}, Input: input})
	if _, err := entry.HandleMessage(t.Context(), "plain input"); err != nil {
		t.Fatal(err)
	}
	if input.text != "plain input" {
		t.Fatalf("input=%+v", input)
	}
}
