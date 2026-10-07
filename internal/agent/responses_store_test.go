package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/config"
	"elbot/internal/hook"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/modelmgr"
	"elbot/internal/storage/sqlite"
	"elbot/internal/tool"
)

type nativeLogWriter struct{ safeStringBuilder }

func (w *nativeLogWriter) Write(p []byte) (int, error) { return w.WriteString(string(p)) }

func configureNativeStore(t *testing.T, opts *testAgentOptions, scope string, value bool) {
	t.Helper()
	var baseURL string
	for _, origin := range opts.Models.ProviderOrigins() {
		if origin.Provider == "native" {
			baseURL = origin.BaseURL
		}
	}
	provider, models := map[string]any{"store": value}, map[string]map[string]any(nil)
	if scope == "model" {
		provider, models = nil, map[string]map[string]any{"m": {"store": value}}
	}
	client, err := api.New(baseURL, "", provider, models, api.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	opts.Models = newTestModels(t, modelmgr.Options{Clients: map[string]llm.Client{"native": client}, Providers: map[string]config.ProviderConfig{"native": {APIMode: "response", BaseURL: baseURL, Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "native", Model: "m"}}})
}

func emitNativeStore(w http.ResponseWriter, id string, store bool, items ...string) {
	output := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		output = append(output, json.RawMessage(item))
	}
	raw, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "store": store, "output": output}})
	fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", raw)
}

func TestResponsesStoreFalseReplaysToolsAndFollowingTurns(t *testing.T) {
	var logs nativeLogWriter
	var tools, prepared atomic.Int32
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "once", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		tools.Add(1)
		return &tool.Result{Content: "durable tool result"}, nil
	}})
	manager := hook.NewManager()
	_ = manager.Register(hook.Registration{Point: hook.PointLLMRequestPrepared, Name: "count", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, error) { prepared.Add(1); return event, nil })})
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		if request.PreviousResponseID != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"No tool call found for function call output"}}`)
			return
		}
		if index == 0 {
			if !request.Store {
				t.Error("default request should enable storage")
			}
			emitNativeStore(w, "r1", false, `{"type":"reasoning","encrypted_content":"opaque-reasoning","vendor_value":9007199254740993}`, `{"type":"future_state","encrypted_content":"opaque-future"}`, nativeCall("call", "once", `{}`))
			return
		}
		body := inputJSON(request)
		for _, fact := range []string{"first input", "opaque-reasoning", "9007199254740993", "opaque-future", `"type":"function_call"`, "durable tool result"} {
			if strings.Count(body, fact) != 1 {
				t.Errorf("fact %q must occur once: %s", fact, body)
			}
		}
		if request.Store {
			t.Error("detected stateless request enabled storage")
		}
		if index > 1 && (strings.Count(body, "first answer") != 1 || strings.Count(body, "new input") != 1) {
			t.Errorf("following turn lost or duplicated history: %s", body)
		}
		emitNativeStore(w, fmt.Sprintf("r%d", index+1), false, nativeText("first answer"))
	}, func(opts *testAgentOptions) {
		opts.ToolRegistry, opts.HookManager = registry, manager
		opts.Logs = componentLogs{logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))}
	})
	if err := f.agent.HandleMessage(t.Context(), "@tool:once first input"); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "new input"); err != nil {
		t.Fatal(err)
	}
	if tools.Load() != 1 || prepared.Load() != 3 || len(f.captured()) != 3 {
		t.Fatalf("tools=%d hooks=%d requests=%d", tools.Load(), prepared.Load(), len(f.captured()))
	}
	log := logs.String()
	if strings.Count(log, "level=WARN") != 1 || !strings.Contains(log, "provider=native") || !strings.Contains(log, "model=m") || !strings.Contains(log, "session_id=") || !strings.Contains(log, "store:false") {
		t.Fatalf("automatic storage log=%s", log)
	}
	for _, output := range []string{f.platform.out.String(), f.platform.preview.String(), f.platform.reasoning.String()} {
		if strings.Contains(output, "store") || strings.Contains(output, "stateless") || strings.Contains(output, "无状态") {
			t.Fatalf("storage notice reached frontend: %s", output)
		}
	}
}

func TestResponsesStorageModes(t *testing.T) {
	for _, mode := range []string{"provider false", "model false", "false response true", "stored", "omitted"} {
		t.Run(mode, func(t *testing.T) {
			explicitFalse := strings.Contains(mode, "false")
			var logs nativeLogWriter
			f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
				if request.Store == explicitFalse {
					t.Errorf("store=%v explicitFalse=%v", request.Store, explicitFalse)
				}
				if index > 0 {
					if explicitFalse {
						if request.PreviousResponseID != "" || len(request.Input) != 3 {
							t.Errorf("stateless request=%+v", request)
						}
					} else if request.PreviousResponseID != "r0" || len(request.Input) != 1 {
						t.Errorf("stored request=%+v", request)
					}
				}
				if mode == "stored" || mode == "false response true" {
					emitNativeStore(w, fmt.Sprintf("r%d", index), true, nativeText("answer"))
				} else {
					// An omitted field must use the actual request's storage preference.
					emitNative(w, fmt.Sprintf("r%d", index), "completed", nativeText("answer"))
				}
			}, func(opts *testAgentOptions) {
				if explicitFalse {
					scope := "provider"
					if mode == "model false" {
						scope = "model"
					}
					configureNativeStore(t, opts, scope, false)
				}
				opts.Logs = componentLogs{logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))}
			})
			for _, input := range []string{"first", "next"} {
				if err := f.agent.HandleMessage(t.Context(), input); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(logs.String(), "stateless") {
				t.Fatalf("unexpected warning: %s", logs.String())
			}
		})
	}
}

func TestResponsesStatelessRestartAfterFailedRequest(t *testing.T) {
	var opts testAgentOptions
	path := filepath.Join(t.TempDir(), "sessions.db")
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		if index == 2 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"temporary request failure"}}`)
			return
		}
		if index > 0 && (request.PreviousResponseID != "" || request.Store) {
			t.Errorf("stateless request=%+v", request)
		}
		if index == 3 {
			body := inputJSON(request)
			for _, fact := range []string{"first input", "second input", "failed input", "retry input", "answer-0", "answer-1"} {
				if strings.Count(body, fact) != 1 {
					t.Errorf("restart fact %q: %s", fact, body)
				}
			}
		}
		emitNativeStore(w, fmt.Sprintf("r%d", index), false, nativeText(fmt.Sprintf("answer-%d", index)))
	}, func(options *testAgentOptions) {
		store, err := sqlite.New(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		options.Store = store
		opts = *options
	})
	f.store = opts.Store
	for _, input := range []string{"first input", "second input"} {
		if err := f.agent.HandleMessage(t.Context(), input); err != nil {
			t.Fatal(err)
		}
	}
	source := fixtureSession(t, f)
	before, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "failed input"); err == nil {
		t.Fatal("expected failure")
	}
	after, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), source.ID)
	if err != nil || after.ID != before.ID {
		t.Fatalf("failed request advanced checkpoint: %+v %v", after, err)
	}
	if err := f.agent.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.execution.sessions.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := opts.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.New(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	opts.Store, f.store = store, store
	// Recreate the client, route, and session service as well as reopening SQLite.
	configureNativeStore(t, &opts, "provider", true)
	f.agent = mustNewWithOptions(t, opts)
	if _, err := f.agent.execution.sessions.Resume(t.Context(), f.agent.Scope(t.Context()), source.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "retry input"); err != nil {
		t.Fatal(err)
	}
	if len(f.captured()) != 4 {
		t.Fatalf("unexpected automatic retry: %d requests", len(f.captured()))
	}
}

func TestResponsesStatelessRejectsIncompleteReplayMaterial(t *testing.T) {
	for _, item := range []string{`{"type":"reasoning","summary":[]}`, `{"type":"message","id":"reference-only","role":"assistant"}`} {
		t.Run(item, func(t *testing.T) {
			f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
				if index != 0 {
					t.Error("incomplete replay reached upstream")
				}
				emitNativeStore(w, "r1", false, item, nativeText("first answer"))
			})
			if err := f.agent.HandleMessage(t.Context(), "first input"); err != nil {
				t.Fatal(err)
			}
			source := fixtureSession(t, f)
			before, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), source.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.agent.HandleMessage(t.Context(), "next input"); err == nil {
				t.Fatal("incomplete native material was accepted")
			}
			after, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), source.ID)
			if err != nil || after.ID != before.ID || len(f.captured()) != 1 {
				t.Fatalf("checkpoint=%+v requests=%d error=%v", after, len(f.captured()), err)
			}
		})
	}
}
