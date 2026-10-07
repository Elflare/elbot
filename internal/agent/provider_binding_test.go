package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestResponseBindingRunsNativeDialogueAndContinuesCheckpoint(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests = append(requests, body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r%d\",\"status\":\"completed\",\"output\":[{\"type\":\"reasoning\",\"id\":\"reasoning\",\"summary\":[],\"encrypted_content\":\"opaque\",\"unknown\":true},{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"native answer\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":4,\"total_tokens\":7}}}\n\n", len(requests))
	}))
	t.Cleanup(server.Close)
	client, err := responses.New(server.URL, "", nil, nil, responses.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	opts := validConstructorOptions(t)
	opts.Models = newTestModels(t, modelmgr.Options{
		Clients: map[string]llm.Client{"native": client}, Providers: map[string]config.ProviderConfig{"native": {APIMode: "response", BaseURL: server.URL}},
		ModeModels: map[string]config.ModelSelection{"work": {Provider: "native", Model: "m"}},
	})
	opts.SessionConfig.NamingConfig.TriggerStep = 100
	a := mustNewWithOptions(t, opts)
	ctx := t.Context()
	for _, text := range []string{"first", "second"} {
		if err := a.HandleMessage(ctx, text); err != nil {
			t.Fatal(err)
		}
	}
	row, err := a.execution.sessions.Current(ctx, a.identity.Scope(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if origin, known, err := session.Origin(row); err != nil || !known || origin.APIType != llm.APITypeResponse {
		t.Fatalf("native origin: %+v / %v", row, err)
	}
	if messages, err := opts.Store.Messages().ListBySession(ctx, row.ID); err != nil || len(messages) != 4 || messages[3].Content != "native answer" {
		t.Fatalf("native display history: %+v / %v", messages, err)
	}
	if len(a.execution.requests.List()) != 0 {
		t.Fatal("native request leaked")
	}
	if len(requests) != 2 || requests[0]["store"] != true || requests[1]["previous_response_id"] != "r1" || requests[0]["instructions"] != requests[1]["instructions"] || len(requests[1]["input"].([]any)) != 1 {
		t.Fatalf("native chain: %+v", requests)
	}
	checkpoint, err := opts.Store.Dialogues().CurrentCheckpoint(ctx, row.ID)
	if err != nil || checkpoint == nil || checkpoint.ResponseID != "r2" || checkpoint.MessageID == "" {
		t.Fatalf("checkpoint=%+v %v", checkpoint, err)
	}
	exchange, err := opts.Store.Dialogues().GetExchange(ctx, checkpoint.ExchangeID)
	if err != nil || !strings.Contains(exchange.ResponseJSON, "encrypted_content") || !strings.Contains(exchange.ItemsJSON, "unknown") || !strings.Contains(exchange.RequestJSON, "second") {
		t.Fatalf("exchange=%+v %v", exchange, err)
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
	if earlyModel || len(models) != 1 || models[0] != (contextinfo.Model{Provider: "default", Model: "fixed", APIType: "chat"}) {
		t.Fatalf("model phase facts = %v / %+v", earlyModel, models)
	}
	facts := executions[0]
	if facts.SessionID == "" || facts.RunID == "" || facts.Attempt == "" || facts.RequestID == "" || facts.RootRequestID == "" || facts.ParentRequestID != facts.RootRequestID || facts.RequestID == facts.RootRequestID {
		t.Fatalf("Hook and main request associations were confused: %+v", facts)
	}
}
