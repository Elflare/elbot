package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

func nativeChainError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprint(w, `{"error":{"code":"previous_response_not_found","param":"previous_response_id","message":"previous response expired"}}`)
}

func fixtureSession(t *testing.T, f *nativeFixture) *storage.Session {
	t.Helper()
	row, err := f.agent.execution.sessions.Current(t.Context(), f.agent.Scope(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func inputJSON(request nativeTestRequest) string {
	raw, _ := json.Marshal(request.Input)
	return string(raw)
}

func TestResponsesRecoveryReplaysNativeFactsOnceWithoutHooksOrTools(t *testing.T) {
	var tools, prepared atomic.Int32
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "once", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		tools.Add(1)
		return &tool.Result{Content: "durable tool result"}, nil
	}})
	manager := hook.NewManager()
	_ = manager.Register(hook.Registration{Point: hook.PointLLMRequestPrepared, Name: "count", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, error) { prepared.Add(1); return event, nil })})
	ids := make(chan string, 16)
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		switch index {
		case 0:
			emitNative(w, "r1", "completed", `{"type":"reasoning","encrypted_content":"opaque-reasoning","vendor_value":9007199254740993}`, `{"type":"future_state","encrypted_content":"opaque-future"}`, nativeCall("call", "once", `{}`))
		case 1:
			emitNative(w, "r2", "completed", nativeText("first answer"))
		case 2:
			if request.PreviousResponseID != "r2" {
				t.Errorf("old chain=%s", request.PreviousResponseID)
			}
			nativeChainError(w)
		case 3:
			body := inputJSON(request)
			if request.PreviousResponseID != "" || !strings.Contains(body, "opaque-reasoning") || !strings.Contains(body, "9007199254740993") || !strings.Contains(body, "opaque-future") || !strings.Contains(body, "durable tool result") || strings.Count(body, "new input") != 1 {
				t.Errorf("replay=%+v", request)
			}
			emitNative(w, "r3", "completed", nativeText("restored answer"))
		default:
			t.Errorf("unexpected recovery attempt %d", index)
		}
	}, func(opts *testAgentOptions) {
		opts.ToolRegistry, opts.HookManager = registry, manager
		opts.Store = nativeAuditStore{Store: opts.Store, repository: nativeAuditRepository{DialogueRepository: opts.Store.Dialogues(), created: ids}}
	})
	if err := f.agent.HandleMessage(t.Context(), "@tool:once first input"); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "new input"); err != nil {
		t.Fatal(err)
	}
	if tools.Load() != 1 || prepared.Load() != 3 {
		t.Fatalf("tools=%d hooks=%d", tools.Load(), prepared.Load())
	}
	requests := f.captured()
	if len(requests) != 4 || requests[2].Instructions != requests[3].Instructions || requests[2].Model != requests[3].Model {
		t.Fatalf("requests=%+v", requests)
	}
	for i := 0; i < 4; i++ {
		id := <-ids
		exchange, err := f.store.Dialogues().GetExchange(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 && exchange.Status != "failed" || i != 2 && exchange.Status != "completed" {
			t.Fatalf("audit %d=%+v", i, exchange)
		}
	}
	cp, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), fixtureSession(t, f).ID)
	if err != nil || cp.ResponseID != "r3" {
		t.Fatalf("checkpoint=%+v %v", cp, err)
	}
}

func TestResponsesRecoveryRejectsMissingReasoningAndDoesNotRetryOtherErrors(t *testing.T) {
	for _, mode := range []string{"missing reasoning", "missing message", "auth", "unrelated 400", "second chain failure", "partial stream"} {
		t.Run(mode, func(t *testing.T) {
			f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
				if index == 0 {
					if mode == "missing reasoning" {
						emitNative(w, "r1", "completed", `{"type":"reasoning","summary":[]}`, nativeText("first"))
					} else if mode == "missing message" {
						emitNative(w, "r1", "completed", `{"type":"message","id":"only-reference","role":"assistant"}`)
					} else {
						emitNative(w, "r1", "completed", `{"type":"reasoning","encrypted_content":"opaque"}`, nativeText("first"))
					}
					return
				}
				switch mode {
				case "auth":
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, `{"error":{"code":"invalid_api_key","message":"bad key"}}`)
				case "unrelated 400":
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"error":{"code":"invalid_request","param":"model","message":"previous response expired"}}`)
				case "partial stream":
					fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\nevent: error\ndata: {\"type\":\"error\",\"code\":\"previous_response_not_found\",\"param\":\"previous_response_id\",\"message\":\"expired\"}\n\n")
				default:
					nativeChainError(w)
				}
			})
			if err := f.agent.HandleMessage(t.Context(), "first input"); err != nil {
				t.Fatal(err)
			}
			row := fixtureSession(t, f)
			before, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), row.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.agent.HandleMessage(t.Context(), "new input"); err == nil {
				t.Fatal("expected failure")
			}
			want := 2
			if mode == "second chain failure" {
				want = 3
			}
			if len(f.captured()) != want {
				t.Fatalf("attempts=%d want=%d", len(f.captured()), want)
			}
			after, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), row.ID)
			if err != nil || after.ID != before.ID {
				t.Fatalf("advanced failed checkpoint=%+v %v", after, err)
			}
		})
	}
}

func TestResponsesForkToolCheckpointExcludesLaterResultsAndKeepsNativeRoot(t *testing.T) {
	var executed atomic.Int32
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "once", run: func(_ context.Context, request tool.CallRequest) (*tool.Result, error) {
		executed.Add(1)
		if strings.Contains(string(request.Arguments), "later") {
			return &tool.Result{Content: "future source result"}, nil
		}
		return &tool.Result{Content: "saved at fork"}, nil
	}})
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		switch index {
		case 0:
			emitNative(w, "head", "completed", `{"type":"reasoning","encrypted_content":"opaque-at-head"}`, nativeCall("call", "once", `{}`), nativeCall("later", "once", `{"later":true}`))
		case 1:
			emitNative(w, "source-final", "completed", nativeText("future source answer"))
		case 2:
			if request.PreviousResponseID != "head" || !strings.Contains(inputJSON(request), "not executed in this branch") || strings.Contains(inputJSON(request), "future source") || strings.Count(inputJSON(request), "saved at fork") != 1 {
				t.Errorf("branch=%+v", request)
			}
			nativeChainError(w)
		case 3:
			body := inputJSON(request)
			if request.PreviousResponseID != "" || !strings.Contains(body, "opaque-at-head") || !strings.Contains(body, "not executed in this branch") || strings.Contains(body, "future source") || strings.Count(body, "branch question") != 1 || strings.Count(body, "saved at fork") != 1 {
				t.Errorf("branch replay=%+v", request)
			}
			emitNative(w, "branch-final", "completed", nativeText("branch answer"))
		default:
			t.Errorf("unexpected request %d", index)
		}
	}, func(opts *testAgentOptions) { opts.ToolRegistry = registry })
	if err := f.agent.HandleMessage(t.Context(), "@tool:once source input"); err != nil {
		t.Fatal(err)
	}
	source := fixtureSession(t, f)
	messages, err := f.store.Messages().ListBySession(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	head := messages[1]
	cp, err := f.store.Dialogues().CheckpointForMessage(t.Context(), source.ID, head.ID)
	if err != nil || !strings.Contains(cp.CallsJSON, "completed") || !strings.Contains(cp.CallsJSON, "pending") {
		t.Fatalf("mutable snapshot=%+v %v", cp, err)
	}
	fork, err := f.agent.execution.sessions.Fork(t.Context(), f.agent.Scope(t.Context()), head.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fork.ForkFromMessageID != messages[2].ID {
		t.Fatalf("fork cut through completed pair: %+v", fork)
	}
	seed, err := f.store.Dialogues().Seed(t.Context(), fork.ID)
	if err != nil || seed.ResponseID != "head" || seed.Consumed || strings.Contains(seed.ItemsJSON, "future source") || strings.Count(seed.ItemsJSON, "saved at fork") != 1 {
		t.Fatalf("seed=%+v %v", seed, err)
	}
	if err := f.store.Sessions().Delete(t.Context(), source.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "branch question"); err != nil {
		t.Fatal(err)
	}
	seed, err = f.store.Dialogues().Seed(t.Context(), fork.ID)
	if err != nil || !seed.Consumed || executed.Load() != 2 {
		t.Fatalf("seed=%+v executions=%d err=%v", seed, executed.Load(), err)
	}
}

func emitCompact(w http.ResponseWriter, items ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	output := make([]json.RawMessage, 0, len(items))
	for index, item := range items {
		output = append(output, json.RawMessage(item))
		fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":%s}\n\n", index, item)
	}
	raw, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "compact-result", "status": "completed", "output": output, "usage": map[string]int{"input_tokens": 7, "output_tokens": 2, "total_tokens": 9}}})
	fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", raw)
}

func expectCompactionRequest(t *testing.T, request nativeTestRequest) {
	t.Helper()
	if request.Endpoint != "/responses" || request.Store || request.PreviousResponseID != "" || string(request.ToolChoice) != `"none"` ||
		len(request.Input) == 0 || string(request.Input[len(request.Input)-1]) != `{"type":"compaction_trigger"}` ||
		strings.Count(inputJSON(request), "compaction_trigger") != 1 {
		t.Errorf("invalid compaction request=%+v", request)
	}
}

func TestResponsesCompactionUsesCurrentModelAndOnlyCompactionThenStartsNewChain(t *testing.T) {
	for _, mode := range []string{"stored", "stateless"} {
		t.Run(mode, func(t *testing.T) {
			stateless := mode == "stateless"
			f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
				switch index {
				case 0:
					emitNativeStore(w, "old", !stateless, `{"type":"reasoning","encrypted_content":"old-reasoning"}`, nativeText("source answer"))
				case 1:
					expectCompactionRequest(t, request)
					if request.Model != "m" || !strings.Contains(inputJSON(request), "old-reasoning") || !strings.Contains(inputJSON(request), "source input") {
						t.Errorf("compact request=%+v", request)
					}
					emitCompact(w, nativeText("audit only"), `{"type":"compaction","encrypted_content":"compact-opaque","vendor_field":{"keep":true},"unknown":9007199254740993}`, `{"type":"future_state","payload":"audit extra"}`)
				case 2:
					body := inputJSON(request)
					if request.PreviousResponseID != "" || len(request.Input) != 2 || !strings.Contains(body, "compact-opaque") || !strings.Contains(body, "9007199254740993") {
						t.Errorf("new root request=%+v", request)
					}
					for _, unwanted := range []string{"source input", "source answer", "old-reasoning", "audit only", "audit extra", "compaction_trigger"} {
						if strings.Contains(body, unwanted) {
							t.Errorf("new root retained %s: %s", unwanted, body)
						}
					}
					emitNativeStore(w, "new-root", !stateless, nativeText("new answer"))
				case 3:
					if stateless {
						if request.PreviousResponseID != "" || request.Store || len(request.Input) != 4 || strings.Count(inputJSON(request), "compact-opaque") != 1 {
							t.Errorf("stateless compact continuation=%+v", request)
						}
					} else if request.PreviousResponseID != "new-root" || len(request.Input) != 1 {
						t.Errorf("continuation=%+v", request)
					}
					emitNative(w, "next", "completed", nativeText("next answer"))
				default:
					t.Errorf("unexpected request %d", index)
				}
			})
			if err := f.agent.HandleMessage(t.Context(), "source input"); err != nil {
				t.Fatal(err)
			}
			old := fixtureSession(t, f)
			if _, err := f.agent.execution.models.SelectCompactModel("native/new-model"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.agent.CompactCurrent(t.Context(), "manual"); err != nil {
				t.Fatal(err)
			}
			next := fixtureSession(t, f)
			if next.ID == old.ID || next.ParentSessionID != "" || strings.Contains(next.Metadata, "llm_checkpoint") {
				t.Fatalf("new session=%+v", next)
			}
			seed, err := f.store.Dialogues().Seed(t.Context(), next.ID)
			if err != nil || seed.ResponseID != "" || seed.Consumed || !strings.Contains(seed.ItemsJSON, "compact-opaque") {
				t.Fatalf("seed=%+v %v", seed, err)
			}
			var saved struct{ Items []json.RawMessage }
			if err := json.Unmarshal([]byte(seed.ItemsJSON), &saved); err != nil || len(saved.Items) != 1 {
				t.Fatalf("seed must contain only compaction: %s err=%v", seed.ItemsJSON, err)
			}
			if err := f.agent.HandleMessage(t.Context(), "first after compact"); err != nil {
				t.Fatal(err)
			}
			seed, err = f.store.Dialogues().Seed(t.Context(), next.ID)
			if err != nil || !seed.Consumed {
				t.Fatalf("seed=%+v %v", seed, err)
			}
			row, err := f.store.Sessions().Get(t.Context(), next.ID)
			if err != nil || strings.Contains(row.Metadata, `"pending":true`) {
				t.Fatalf("pending compression=%+v %v", row, err)
			}
			if err := f.agent.HandleMessage(t.Context(), "next input"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResponsesCompactionFailureKeepsOriginalSession(t *testing.T) {
	for _, mode := range []string{"unsupported", "missing encrypted", "store failure"} {
		t.Run(mode, func(t *testing.T) {
			var fail atomic.Bool
			f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
				if index == 0 {
					emitNative(w, "old", "completed", nativeText("answer"))
					return
				}
				if mode == "unsupported" {
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, `{"error":{"code":"unsupported","message":"compact unavailable"}}`)
					return
				}
				if mode == "missing encrypted" {
					emitCompact(w, `{"type":"compaction"}`)
					return
				}
				emitCompact(w, `{"type":"compaction","encrypted_content":"opaque"}`)
			}, func(opts *testAgentOptions) {
				if mode == "store failure" {
					opts.Store = nativeCreateFailureStore{Store: opts.Store, fail: &fail}
				}
			})
			if err := f.agent.HandleMessage(t.Context(), "source input"); err != nil {
				t.Fatal(err)
			}
			old, binding, err := f.agent.execution.sessions.CurrentBound(t.Context(), f.agent.Scope(t.Context()))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "store failure" {
				fail.Store(true)
			}
			if _, err := f.agent.CompactCurrent(t.Context(), "manual"); err == nil {
				t.Fatal("expected compact failure")
			}
			current, after, err := f.agent.execution.sessions.CurrentBound(t.Context(), f.agent.Scope(t.Context()))
			if err != nil || current.ID != old.ID || after != binding || !binding.Valid() {
				t.Fatalf("changed failed session=%+v %v", current, err)
			}
		})
	}
}

func TestResponsesRecoveryRefusesMissingMedia(t *testing.T) {
	var center *media.Manager
	var id string
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "image", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		return &tool.Result{Segments: []llm.MessageSegment{{Type: llm.SegmentImage, MediaID: id}}}, nil
	}})
	f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
		if index == 0 {
			emitNative(w, "head", "completed", nativeCall("call", "image", `{}`))
			return
		}
		if index == 1 {
			emitNative(w, "final", "completed", nativeText("answer"))
			return
		}
		nativeChainError(w)
	}, func(opts *testAgentOptions) {
		root := filepath.Join(t.TempDir(), "media")
		center = media.NewManager(opts.Store, root, &media.LocalBackend{Root: root})
		opts.Media, opts.ToolRegistry = center, registry
	})
	item, err := center.ImportBytes(t.Context(), []byte("image"), media.Input{MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	id = item.ID
	if err := f.agent.HandleMessage(t.Context(), "@tool:image source input"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(item.LocalPath); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "new input"); err == nil {
		t.Fatal("missing media was converted to text")
	}
	if len(f.captured()) != 3 {
		t.Fatalf("attempts=%d", len(f.captured()))
	}
}

// Additional lifecycle checks use the real Session service and provider wiring.
func TestResponsesBackgroundCopyContinuesNativeChainWithoutAdoptingCurrent(t *testing.T) {
	for _, mode := range []string{"stored", "stateless"} {
		t.Run(mode, func(t *testing.T) {
			stateless := mode == "stateless"
			f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
				if index == 0 {
					emitNativeStore(w, "background", !stateless, `{"type":"reasoning","encrypted_content":"background-opaque"}`, nativeText(`{"completed":true,"need_report":false}`))
					return
				}
				if stateless {
					if request.PreviousResponseID != "" || strings.Count(inputJSON(request), "background-opaque") != 1 {
						t.Errorf("stateless copy=%+v", request)
					}
				} else if request.PreviousResponseID != "background" {
					t.Errorf("copy continuation=%+v", request)
				}
				emitNative(w, "copy-answer", "completed", nativeText("continued copy"))
			})
			actor := contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin}
			result, err := f.agent.RunBackground(t.Context(), background.RunRequest{Kind: background.KindCron, Name: "copy", Actor: actor, Platform: "cli", Prompt: "background input"})
			if err != nil {
				t.Fatal(err)
			}
			foreground, err := f.agent.execution.sessions.Create(t.Context(), f.agent.Scope(t.Context()), session.CreateRequest{})
			if err != nil {
				t.Fatal(err)
			}
			_, binding, err := f.agent.execution.sessions.CurrentBound(t.Context(), f.agent.Scope(t.Context()))
			if err != nil {
				t.Fatal(err)
			}
			copy, err := f.agent.execution.sessions.CopyBackground(t.Context(), session.Scope{ActorID: actor.ID, Platform: "cli", PlatformScopeID: "cron:copy"}, session.BackgroundCopyRequest{SourceSessionID: result.SessionID, Kind: "cron", Name: "copy"})
			if err != nil {
				t.Fatal(err)
			}
			current, after, err := f.agent.execution.sessions.CurrentBound(t.Context(), f.agent.Scope(t.Context()))
			if err != nil || current.ID != foreground.ID || after != binding {
				t.Fatalf("copy adopted foreground=%+v %v", current, err)
			}
			seed, err := f.store.Dialogues().Seed(t.Context(), copy.ID)
			if err != nil || !strings.Contains(seed.ItemsJSON, "background-opaque") {
				t.Fatalf("seed=%+v %v", seed, err)
			}
			if stateless && seed.ResponseID != "" {
				t.Fatalf("stateless copy response ID=%s", seed.ResponseID)
			}
			if err := f.store.Sessions().Delete(t.Context(), result.SessionID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.agent.execution.sessions.Resume(t.Context(), f.agent.Scope(t.Context()), copy.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.agent.HandleMessage(t.Context(), "continue copy"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func addNativeOtherModels(t *testing.T, opts *testAgentOptions, workProvider string) {
	t.Helper()
	var nativeOrigin llm.Origin
	for _, origin := range opts.Models.ProviderOrigins() {
		if origin.Provider == "native" {
			nativeOrigin = origin
		}
	}
	client := opts.Models.ClientForProvider("native")
	chat := &fakeLLM{models: []string{"chat-model"}}
	opts.Models = newTestModels(t, modelmgr.Options{Clients: map[string]llm.Client{"native": client, "other": client, "chat": chat}, Providers: map[string]config.ProviderConfig{
		"native": {APIMode: "response", BaseURL: nativeOrigin.BaseURL, Models: []string{"m", "new-model", "task-model"}},
		"other":  {APIMode: "response", BaseURL: nativeOrigin.BaseURL, Models: []string{"m", "new-model"}},
		"chat":   {Models: []string{"chat-model"}},
	}, ModeModels: map[string]config.ModelSelection{"work": {Provider: workProvider, Model: "m"}, "chat": {Provider: "chat", Model: "chat-model"}}})
}

func TestResponsesModelCommandPreflightsOnlyAffectedSlotAndRequestChecksGlobalChanges(t *testing.T) {
	f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
		emitNative(w, fmt.Sprintf("r%d", index+1), "completed", nativeText("answer"))
	}, func(opts *testAgentOptions) { addNativeOtherModels(t, opts, "native") })
	if err := f.agent.HandleMessage(t.Context(), "first input"); err != nil {
		t.Fatal(err)
	}
	row := fixtureSession(t, f)
	for _, arg := range []string{"/model other/m", "/model chat/chat-model"} {
		if err := f.agent.HandleMessage(t.Context(), arg); err == nil {
			t.Fatalf("accepted incompatible command %s", arg)
		}
		if selected := f.agent.execution.models.ResolveMode("work"); selected.Provider != "native" || selected.Model != "m" {
			t.Fatalf("failed command changed selection=%+v", selected)
		}
	}
	if err := f.agent.HandleMessage(t.Context(), "/model --chat chat/chat-model"); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "/model native/new-model"); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "second input"); err != nil {
		t.Fatal(err)
	}
	requests := f.captured()
	if len(requests) != 2 || requests[1].Model != "new-model" || requests[1].PreviousResponseID != "r1" {
		t.Fatalf("same vendor switch=%+v", requests)
	}
	f.agent.execution.contexts.Configure(config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: 0.5}, config.ModelMetadataConfig{DefaultContextWindow: 10}, nil)
	if _, err := f.agent.execution.models.SelectModelForMode("work", "other/m"); err != nil {
		t.Fatal(err)
	}
	before, err := f.store.Messages().ListBySession(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "rejected input"); err == nil {
		t.Fatal("global change bypassed compatibility")
	}
	after, err := f.store.Messages().ListBySession(t.Context(), row.ID)
	if err != nil || len(after) != len(before) || len(f.captured()) != 2 {
		t.Fatalf("incompatible request wrote input or compacted: messages=%d requests=%d err=%v", len(after), len(f.captured()), err)
	}
}

func TestResponsesIncompatibleTakeoverKeepsBackgroundExecutionAndForegroundBinding(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
		close(started)
		<-release
		emitNative(w, "background", "completed", nativeText(`{"completed":true,"need_report":false}`))
	}, func(opts *testAgentOptions) { addNativeOtherModels(t, opts, "other") })
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	foreground, err := f.agent.execution.sessions.Create(t.Context(), f.agent.Scope(t.Context()), session.CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, binding, err := f.agent.execution.sessions.CurrentBound(t.Context(), f.agent.Scope(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.agent.RunBackground(t.Context(), background.RunRequest{Kind: background.KindCron, Name: "takeover", Actor: contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin}, Platform: "cli", Prompt: "task input", ModelProvider: "native", Model: "m"})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background did not start")
	}
	requests := f.agent.execution.requests.List()
	if len(requests) != 1 {
		t.Fatalf("active requests=%+v", requests)
	}
	id := requests[0].SessionID
	if _, err := f.agent.execution.sessions.Resume(t.Context(), f.agent.Scope(t.Context()), id); err == nil {
		t.Fatal("incompatible takeover was accepted")
	}
	row, err := f.store.Sessions().Get(t.Context(), id)
	if err != nil || !session.IsBackground(row) || session.WasPromoted(row) {
		t.Fatalf("background changed=%+v %v", row, err)
	}
	current, after, err := f.agent.execution.sessions.CurrentBound(t.Context(), f.agent.Scope(t.Context()))
	if err != nil || current.ID != foreground.ID || after != binding || !binding.Valid() {
		t.Fatalf("foreground changed=%+v %v", current, err)
	}
	once.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("background did not finish")
	}
}

func TestResponsesAutomaticCompactionUsesSharedThresholdAndPreservesAcceptedInput(t *testing.T) {
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		switch index {
		case 0:
			emitNative(w, "old", "completed", nativeText("first answer"))
		case 1:
			expectCompactionRequest(t, request)
			emitCompact(w, `{"type":"compaction","encrypted_content":"auto-opaque"}`)
		case 2:
			if request.PreviousResponseID != "" || !strings.Contains(inputJSON(request), "auto-opaque") || strings.Count(inputJSON(request), "accepted second input") != 1 {
				t.Errorf("handoff request=%+v", request)
			}
			emitNative(w, "new", "completed", nativeText("second answer"))
		default:
			t.Errorf("unexpected request %d", index)
		}
	})
	if err := f.agent.HandleMessage(t.Context(), "first input"); err != nil {
		t.Fatal(err)
	}
	old := fixtureSession(t, f)
	f.agent.execution.contexts.Configure(config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: 0.5}, config.ModelMetadataConfig{DefaultContextWindow: 10}, nil)
	if err := f.agent.HandleMessage(t.Context(), "accepted second input"); err != nil {
		t.Fatal(err)
	}
	if current := fixtureSession(t, f); current.ID == old.ID {
		t.Fatal("automatic compaction did not hand off")
	}
	if len(f.captured()) != 3 {
		t.Fatalf("requests=%+v", f.captured())
	}
}

func TestResponsesForkAfterCompactionRetainsMediaAndHistoricalRootAfterSourceDeletion(t *testing.T) {
	var center *media.Manager
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		switch index {
		case 0:
			emitNative(w, "original", "completed", nativeText("original answer"))
		case 1:
			expectCompactionRequest(t, request)
			if !strings.Contains(inputJSON(request), "data:image/png;base64,") {
				t.Errorf("compact input missing media: %+v", request)
			}
			emitCompact(w, `{"type":"compaction","encrypted_content":"media-root-opaque"}`)
		case 2:
			if request.PreviousResponseID != "" || strings.Contains(inputJSON(request), "input_image") || !strings.Contains(inputJSON(request), "media-root-opaque") {
				t.Errorf("compacted root=%+v", request)
			}
			emitNative(w, "root-anchor", "completed", nativeText("anchor answer"))
		case 3:
			emitNative(w, "later-root", "completed", nativeText("exclude later answer"))
		case 4:
			if request.PreviousResponseID != "root-anchor" {
				t.Errorf("historical anchor=%+v", request)
			}
			nativeChainError(w)
		case 5:
			body := inputJSON(request)
			if request.PreviousResponseID != "" || !strings.Contains(body, "media-root-opaque") || strings.Contains(body, "data:image/png;base64,") || strings.Contains(body, "exclude later") || strings.Count(body, "branch input") != 1 {
				t.Errorf("independent root=%+v", request)
			}
			emitNative(w, "branch", "completed", nativeText("branch answer"))
		default:
			t.Errorf("unexpected request %d", index)
		}
	}, func(opts *testAgentOptions) {
		root := filepath.Join(t.TempDir(), "media")
		center = media.NewManager(opts.Store, root, &media.LocalBackend{Root: root})
		opts.Media = center
	})
	image, err := center.ImportBytes(t.Context(), []byte("retained image"), media.Input{MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := platform.WithMessageContext(t.Context(), platform.MessageContext{Segments: []platform.MessageSegment{{Type: platform.SegmentText, Text: "source input"}, {Type: platform.SegmentImage, MediaID: image.ID}}})
	if err := f.agent.HandleMessage(ctx, "source input"); err != nil {
		t.Fatal(err)
	}
	original := fixtureSession(t, f)
	if _, err := f.agent.CompactCurrent(t.Context(), "manual"); err != nil {
		t.Fatal(err)
	}
	root := fixtureSession(t, f)
	rootSeed, err := f.store.Dialogues().Seed(t.Context(), root.ID)
	if err != nil || len(rootSeed.MediaIDs) != 1 || rootSeed.MediaIDs[0] != image.ID || strings.Contains(rootSeed.ItemsJSON, "input_image") || strings.Contains(rootSeed.MaterialsJSON, image.ID) {
		t.Fatalf("retained media seed=%+v %v", rootSeed, err)
	}
	if err := f.agent.HandleMessage(t.Context(), "anchor input"); err != nil {
		t.Fatal(err)
	}
	messages, err := f.store.Messages().ListBySession(t.Context(), root.ID)
	if err != nil {
		t.Fatal(err)
	}
	anchor := messages[len(messages)-1]
	if err := f.agent.HandleMessage(t.Context(), "exclude later input"); err != nil {
		t.Fatal(err)
	}
	branch, err := f.agent.execution.sessions.Fork(t.Context(), f.agent.Scope(t.Context()), anchor.ID)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := f.store.Dialogues().Seed(t.Context(), branch.ID)
	if err != nil || strings.Contains(seed.ItemsJSON, "exclude later") {
		t.Fatalf("historical seed=%+v %v", seed, err)
	}
	for _, id := range []string{original.ID, root.ID} {
		if err := f.store.Sessions().Delete(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := f.store.MediaReferences().ListByOwner(t.Context(), "native_seed", seed.ID)
	if err != nil || len(refs) != 1 || refs[0].MediaID != image.ID {
		t.Fatalf("independent references=%+v %v", refs, err)
	}
	if err := f.agent.HandleMessage(t.Context(), "branch input"); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesCanceledCompactionKeepsOriginalBinding(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
		if index == 0 {
			emitNative(w, "source", "completed", nativeText("source answer"))
			return
		}
		close(started)
		<-release
		emitCompact(w, `{"type":"compaction","encrypted_content":"late-root"}`)
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	if err := f.agent.HandleMessage(t.Context(), "source input"); err != nil {
		t.Fatal(err)
	}
	old, binding, err := f.agent.execution.sessions.CurrentBound(t.Context(), f.agent.Scope(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.agent.CompactCurrent(ctx, "manual"); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("compact did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled compaction succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("compact did not cancel")
	}
	once.Do(func() { close(release) })
	current, after, err := f.agent.execution.sessions.CurrentBound(t.Context(), f.agent.Scope(t.Context()))
	if err != nil || current.ID != old.ID || after != binding || !binding.Valid() {
		t.Fatalf("late compact changed binding=%+v %v", current, err)
	}
}

type nativeTerminalFailureStore struct {
	storage.Store
	fail *atomic.Bool
}

func (s nativeTerminalFailureStore) Dialogues() storage.DialogueRepository {
	return nativeTerminalFailureRepository{DialogueRepository: s.Store.Dialogues(), fail: s.fail}
}

type nativeTerminalFailureRepository struct {
	storage.DialogueRepository
	fail *atomic.Bool
}

func (r nativeTerminalFailureRepository) FinishExchange(ctx context.Context, id, status, response, items, failure string) error {
	if r.fail.Load() {
		return errors.New("terminal audit storage failed")
	}
	return r.DialogueRepository.FinishExchange(ctx, id, status, response, items, failure)
}

func TestResponsesDoesNotRecoverWhenFailedAttemptCannotBeArchived(t *testing.T) {
	var fail atomic.Bool
	f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
		if index == 0 {
			emitNative(w, "original", "completed", nativeText("source answer"))
			return
		}
		nativeChainError(w)
	}, func(opts *testAgentOptions) {
		opts.Store = nativeTerminalFailureStore{Store: opts.Store, fail: &fail}
	})
	if err := f.agent.HandleMessage(t.Context(), "source input"); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	if err := f.agent.HandleMessage(t.Context(), "new input"); err == nil || !strings.Contains(err.Error(), "terminal audit storage failed") {
		t.Fatalf("missing storage failure: %v", err)
	}
	if len(f.captured()) != 2 {
		t.Fatalf("continued after audit failure: %d requests", len(f.captured()))
	}
	cp, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), fixtureSession(t, f).ID)
	if err != nil || cp.ResponseID != "original" {
		t.Fatalf("advanced after audit failure=%+v %v", cp, err)
	}
}

type nativeCreateFailureStore struct {
	storage.Store
	fail *atomic.Bool
}

func (s nativeCreateFailureStore) Sessions() storage.SessionRepository {
	return nativeCreateFailureRepository{SessionRepository: s.Store.Sessions(), fail: s.fail}
}

type nativeCreateFailureRepository struct {
	storage.SessionRepository
	fail *atomic.Bool
}

func (r nativeCreateFailureRepository) CreateMaterial(ctx context.Context, req storage.SessionMaterialCreate) error {
	if r.fail.Load() {
		return errors.New("native seed save failed")
	}
	return r.SessionRepository.CreateMaterial(ctx, req)
}
