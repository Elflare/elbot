package agent

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type constructorHooks struct{}

func (constructorHooks) Run(_ context.Context, e hook.Event) (hook.Event, events.LogResult, error) {
	return e, events.LogResult{ResultStatus: events.ResultSkipped}, nil
}
func (constructorHooks) Notify(context.Context, hook.Event)                     {}
func (constructorHooks) FillContext(_ context.Context, e hook.Event) hook.Event { return e }
func (constructorHooks) NotifyError(context.Context, hook.Event, error)         {}
func (constructorHooks) CancelRoute(hook.Event) bool                            { return false }
func (constructorHooks) Route(_ context.Context, e hook.Event) (hook.Event, bool, error) {
	return e, false, nil
}
func (constructorHooks) HasRoute(hook.Event) bool { return false }

type constructorCommands struct{}

func (constructorCommands) Handle(context.Context, string) (bool, error) { return false, nil }
func (constructorCommands) IsCommand(string) bool                        { return false }

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
	identity := NewIdentityResolver(IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Policy: security.DefaultPolicy()})
	input := &constructorInput{}
	entry := NewMessageHandler(MessageHandlerDeps{Identity: identity, Hooks: constructorHooks{}, Output: constructorOutput{}, Commands: constructorCommands{}, Input: input})
	if err := entry.HandleMessage(t.Context(), "plain input"); err != nil {
		t.Fatal(err)
	}
	if input.text != "plain input" || !input.expired {
		t.Fatalf("input=%+v", input)
	}
}

func TestExecutionConstructionKeepsBorrowedCallbacksAndServices(t *testing.T) {
	opts := assembleTestOptions(t, validConstructorOptions(t))
	defer opts.Sessions.Close(context.Background())
	identity := NewIdentityResolver(IdentityResolverDeps{PlatformName: "cli", ActorID: "cli:local", ScopeID: "local", Policy: opts.SecurityPolicy})
	policy := NewConfirmationPolicy(ConfirmationPolicyDeps{Identity: identity})
	status := NewStatusRecorder(StatusRecorderDeps{Turns: opts.Turns})
	output := NewOutputSender(OutputSenderDeps{Dispatcher: opts.Dispatcher, Hooks: constructorHooks{}})
	activations := 0
	connectTestForeground(t, opts.Sessions, func(context.Context, *storage.Session, *session.Binding) { activations++ })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	deps := ExecutionCoordinatorDeps{
		Sessions:    opts.Sessions,
		SessionRows: opts.Store.Sessions(),
		Turns:       opts.Turns,
		Requests:    opts.Requests,
		Contexts:    opts.Contexts,
		Models:      opts.Models,
		Dialogue:    &dialogue.Runner{Routes: opts.Routes},
		Identity:    identity,
		View:        dialogue.ExecutionView{Sessions: opts.Store.Sessions()},
		Output:      output,
		Status:      status,
		WaitPolicy:  policy,
	}
	execution := NewExecutionCoordinator(ctx, deps)
	scope := identity.Scope(t.Context())
	row, err := opts.Sessions.PrepareBackground(t.Context(), scope, session.BackgroundRequest{Kind: "cron", Name: "borrowed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opts.Sessions.Resume(t.Context(), scope, row.ID); err != nil {
		t.Fatal(err)
	}
	if activations != 1 {
		t.Fatalf("constructor replaced borrowed callback: %d", activations)
	}
	cancel()
	closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	for range 2 {
		if err := execution.Close(closeCtx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := opts.Sessions.Current(closeCtx, scope); err != nil {
		t.Fatalf("close released borrowed Sessions: %v", err)
	}
}

func validConstructorOptions(t *testing.T) testAgentOptions {
	t.Helper()
	return testAgentOptions{
		Models: newTestModels(t, modelmgr.Options{
			Clients:   map[string]llm.Client{"default": &fakeLLM{}},
			Providers: map[string]config.ProviderConfig{"default": {}},
			ModeModels: map[string]config.ModelSelection{
				storage.SessionModeWork: {Provider: "default", Model: "model"},
				storage.SessionModeChat: {Provider: "default", Model: "model"},
			},
			DefaultMode: storage.SessionModeWork,
		}),
		Providers:       map[string]config.ProviderConfig{"default": {}},
		Store:           newTestStore(t),
		CommandPrefixes: []string{"/"},
		SessionConfig:   session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork},
		SecurityPolicy:  security.DefaultPolicy(),
		SandboxRoot:     "data/sandbox",
		ToolsConfig:     config.ToolsConfig{MaxRoundsPerTurn: 2},
	}
}
