package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func TestProviderAssemblyRejectsWrongNativeCapability(t *testing.T) {
	for _, mode := range []string{"chat", "response"} {
		t.Run(mode, func(t *testing.T) {
			var client llm.Client = &fakeLLM{}
			if mode == "chat" {
				var err error
				client, err = responses.New("https://unused.invalid", "", nil, nil, responses.RequestOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
			models := newTestModels(t, modelmgr.Options{
				Clients: map[string]llm.Client{"p": client}, Providers: map[string]config.ProviderConfig{"p": {APIMode: mode}},
				ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}},
			})
			opts := validConstructorOptions(t)
			opts.Models = models
			assembly := assembleTestOptions(opts)
			if _, err := New(t.Context(), assembly.Config, assembly.Dependencies); err == nil || !strings.Contains(err.Error(), "api_mode") || !strings.Contains(err.Error(), "p") {
				t.Fatalf("wrong native capability was accepted: %v", err)
			}
		})
	}
}

func TestResponseBindingRejectsMainDialogueBeforeMutation(t *testing.T) {
	client, err := responses.New("https://unused.invalid", "", nil, nil, responses.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	opts := validConstructorOptions(t)
	opts.Models = newTestModels(t, modelmgr.Options{
		Clients: map[string]llm.Client{"native": client}, Providers: map[string]config.ProviderConfig{"native": {APIMode: "response", BaseURL: "https://unused.invalid"}},
		ModeModels: map[string]config.ModelSelection{"work": {Provider: "native", Model: "m"}},
	})
	a := mustNewWithOptions(t, opts)
	ctx := t.Context()
	if err := a.HandleMessage(ctx, "do not save this"); err == nil || !strings.Contains(err.Error(), "主对话") {
		t.Fatalf("missing dialogue was not rejected before the API call: %v", err)
	}
	row, err := a.execution.sessions.Current(ctx, a.identity.Scope(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if _, known, err := session.Origin(row); err != nil || known {
		t.Fatalf("rejected target registered an origin: %+v / %v", row, err)
	}
	if messages, err := opts.Store.Messages().ListBySession(ctx, row.ID); err != nil || len(messages) != 0 {
		t.Fatalf("rejected target saved input: %+v / %v", messages, err)
	}
	if len(a.execution.requests.List()) != 0 {
		t.Fatal("rejected target registered a request")
	}
}

type originFailureStore struct {
	storage.Store
	repository storage.SessionRepository
}

func (s originFailureStore) Sessions() storage.SessionRepository { return s.repository }

type originFailureRepository struct {
	storage.SessionRepository
	err error
}

func (r originFailureRepository) Mutate(context.Context, string, func(*storage.Session) error) (*storage.Session, error) {
	return nil, r.err
}

func TestOriginSaveFailureStopsBeforeInputAndModelCall(t *testing.T) {
	base := newTestStore(t)
	failure := errors.New("origin write failed")
	store := originFailureStore{Store: base, repository: originFailureRepository{SessionRepository: base.Sessions(), err: failure}}
	client := &fakeLLM{replies: []string{"must not run"}}
	a := newTestAgent(t, &fakePlatform{}, client, "m", config.ProviderConfig{}, store)
	ctx := t.Context()
	if err := a.HandleMessage(ctx, "input"); !errors.Is(err, failure) {
		t.Fatalf("origin failure = %v", err)
	}
	row, err := a.execution.sessions.Current(ctx, a.identity.Scope(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if messages, err := base.Messages().ListBySession(ctx, row.ID); err != nil || len(messages) != 0 || client.requestCount() != 0 || len(a.execution.requests.List()) != 0 {
		t.Fatalf("failed origin reached input or model: %+v / %v", messages, err)
	}
}

func TestHooksReadFixedModelAndActualRequestFacts(t *testing.T) {
	manager := hook.NewManager()
	var earlyModel bool
	var models []contextinfo.Model
	var executions []contextinfo.Execution
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformMessageReceived, Name: "before-selection", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		_, earlyModel = contextinfo.ModelFromContext(ctx)
		return event, nil
	})}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(hook.Registration{Point: hook.PointLLMRequestPrepared, Name: "after-selection", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		model, _ := contextinfo.ModelFromContext(ctx)
		execution, _ := contextinfo.ExecutionFromContext(ctx)
		models = append(models, model)
		executions = append(executions, execution)
		return event, nil
	})}); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{replies: []string{"answer"}}, "fixed", config.ProviderConfig{}, newTestStore(t), func(opts *testAgentOptions) {
		opts.HookManager = manager
		opts.SessionConfig.NamingConfig.TriggerStep = 100
	})
	if err := a.HandleMessage(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	if earlyModel || len(models) != 1 || models[0] != (contextinfo.Model{Provider: "default", Model: "fixed", Protocol: "chat"}) {
		t.Fatalf("model phase facts = %v / %+v", earlyModel, models)
	}
	facts := executions[0]
	if facts.SessionID == "" || facts.RunID == "" || facts.Attempt == "" || facts.RequestID == "" || facts.RootRequestID == "" || facts.ParentRequestID != facts.RootRequestID || facts.RequestID == facts.RootRequestID {
		t.Fatalf("Hook and main request associations were confused: %+v", facts)
	}
}
