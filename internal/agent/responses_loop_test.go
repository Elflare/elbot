package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	api "elbot/internal/llm/responses"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type nativeTestRequest struct {
	Endpoint           string            `json:"-"`
	Model              string            `json:"model"`
	Instructions       string            `json:"instructions"`
	PreviousResponseID string            `json:"previous_response_id"`
	Store              bool              `json:"store"`
	Input              []json.RawMessage `json:"input"`
	Tools              []json.RawMessage `json:"tools"`
}
type nativeFixture struct {
	agent    *Agent
	store    storage.Store
	platform *fakePlatform
	mu       sync.Mutex
	requests []nativeTestRequest
}

func newNativeFixture(t *testing.T, respond func(int, nativeTestRequest, http.ResponseWriter), configure ...func(*testAgentOptions)) *nativeFixture {
	t.Helper()
	f := &nativeFixture{store: newTestStore(t), platform: &fakePlatform{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			fmt.Fprint(w, `{"data":[{"id":"m"},{"id":"new-model"},{"id":"task-model"}]}`)
			return
		}
		var request nativeTestRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		request.Endpoint = r.URL.Path
		f.mu.Lock()
		f.requests = append(f.requests, request)
		index := len(f.requests) - 1
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		respond(index, request, w)
	}))
	t.Cleanup(server.Close)
	client, err := api.New(server.URL, "", nil, nil, api.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	opts := validConstructorOptions(t)
	opts.Store = f.store
	opts.Platform = f.platform
	opts.SessionConfig.NamingConfig.TriggerStep = 100
	opts.Models = newTestModels(t, modelmgr.Options{Clients: map[string]llm.Client{"native": client}, Providers: map[string]config.ProviderConfig{"native": {APIMode: "response", BaseURL: server.URL, Models: []string{"m", "new-model", "task-model"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "native", Model: "m"}, "chat": {Provider: "native", Model: "m"}}})
	f.agent = mustNewWithOptions(t, opts, configure...)
	return f
}
func (f *nativeFixture) captured() []nativeTestRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]nativeTestRequest(nil), f.requests...)
}
func emitNative(w http.ResponseWriter, id, status string, items ...string) {
	output := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		output = append(output, json.RawMessage(item))
	}
	response := map[string]any{"id": id, "status": status, "output": output, "usage": map[string]int{"input_tokens": 3, "output_tokens": 4, "total_tokens": 7}}
	if status == "incomplete" {
		response["incomplete_details"] = map[string]string{"reason": "max_output_tokens"}
	}
	raw, _ := json.Marshal(map[string]any{"type": "response." + status, "response": response})
	fmt.Fprintf(w, "event: response.%s\ndata: %s\n\n", status, raw)
}
func nativeText(text string) string {
	raw, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": text}}})
	return string(raw)
}
func nativeCall(id, name, args string) string {
	raw, _ := json.Marshal(map[string]any{"type": "function_call", "id": "item-" + id, "call_id": id, "name": name, "arguments": args, "status": "completed"})
	return string(raw)
}

func TestResponsesMediaResultsResolveOnlyNewInputs(t *testing.T) {
	var center *media.Manager
	var imageID string
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "image", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		return &tool.Result{Segments: []llm.MessageSegment{{Type: llm.SegmentText, Text: "image result"}, {Type: llm.SegmentImage, MediaID: imageID, Name: "tiny.png"}}}, nil
	}})
	f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
		if index == 0 {
			emitNative(w, "r1", "completed", nativeCall("call", "image", `{}`))
		} else {
			emitNative(w, fmt.Sprintf("r%d", index+1), "completed", nativeText("done"))
		}
	}, func(opts *testAgentOptions) {
		root := filepath.Join(t.TempDir(), "media")
		center = media.NewManager(opts.Store, root, &media.LocalBackend{Root: root})
		opts.Media, opts.ToolRegistry = center, registry
	})
	item, err := center.ImportBytes(t.Context(), []byte("tiny image"), media.Input{Name: "tiny.png", MIMEType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	imageID = item.ID
	if err := f.agent.HandleMessage(t.Context(), "@tool:image run"); err != nil {
		t.Fatal(err)
	}
	requests := f.captured()
	if len(requests) != 2 || len(requests[1].Input) != 1 {
		t.Fatalf("requests=%+v", requests)
	}
	var output struct {
		CallID string             `json:"call_id"`
		Output []api.InputContent `json:"output"`
	}
	if err := json.Unmarshal(requests[1].Input[0], &output); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, part := range output.Output {
		if part.Type == "input_image" && strings.HasPrefix(part.ImageURL, "data:image/png;base64,") {
			found = true
		}
	}
	if output.CallID != "call" || !found {
		t.Fatalf("native media output=%+v", output)
	}
	row, err := f.agent.execution.sessions.Current(t.Context(), f.agent.Scope(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := f.store.Messages().ListBySession(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := f.store.MediaReferences().ListByOwner(t.Context(), "tool_result", messages[2].ID)
	if err != nil || len(refs) != 1 || refs[0].MediaID != imageID {
		t.Fatalf("media references=%+v %v", refs, err)
	}
	center.FileDelivery.MaxDirectBase64Bytes = 1
	if err := f.agent.HandleMessage(t.Context(), "next input"); err != nil {
		t.Fatal(err)
	}
	requests = f.captured()
	if len(requests) != 3 || len(requests[2].Input) != 1 || strings.Contains(string(requests[2].Input[0]), "input_image") {
		t.Fatalf("resent media history=%+v", requests)
	}
}

func TestResponsesPendingAndRoundLimitKeepFixedModelAndSkipUnexecutedCalls(t *testing.T) {
	started, release, unblock := modelBarrier(t)
	count := 0
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "echo", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		count++
		return &tool.Result{Content: "ok"}, nil
	}})
	f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
		switch index {
		case 0:
			emitNative(w, "r1", "completed", nativeCall("one", "echo", `{}`))
		case 1:
			close(started)
			<-release
			emitNative(w, "r2", "completed", nativeText("intermediate"), nativeCall("two", "echo", `{}`))
		default:
			emitNative(w, "r3", "completed", nativeText("summary"))
		}
	}, func(opts *testAgentOptions) { opts.ToolRegistry = registry; opts.ToolsConfig.MaxRoundsPerTurn = 1 })
	t.Cleanup(unblock)
	done := make(chan error, 1)
	go func() { done <- f.agent.HandleMessage(context.Background(), "@tool:echo run") }()
	awaitModelBarrier(t, started)
	if err := f.agent.HandleMessage(t.Context(), "/model --work native/new-model"); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(t.Context(), "pending detail"); err != nil {
		t.Fatal(err)
	}
	unblock()
	awaitModelDone(t, done)
	requests := f.captured()
	if count != 1 || len(requests) != 3 || len(requests[2].Tools) != 0 || requests[2].PreviousResponseID != "r2" {
		t.Fatalf("count=%d requests=%+v", count, requests)
	}
	for _, request := range requests {
		if request.Model != "m" {
			t.Fatalf("in-flight model switched: %+v", requests)
		}
	}
	raw, _ := json.Marshal(requests[2].Input)
	for _, want := range []string{"max_rounds_per_turn", "pending detail", "总结当前进度"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s: %s", want, raw)
		}
	}
	if !strings.Contains(f.platform.out.String(), "intermediate") || !strings.Contains(f.platform.out.String(), "summary") {
		t.Fatalf("output=%s", f.platform.out.String())
	}
}

func TestResponsesBackgroundTakeoverSharesPendingAndChangesTaskModel(t *testing.T) {
	started, release, unblock := modelBarrier(t)
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "slow", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		close(started)
		<-release
		return &tool.Result{Content: "done"}, nil
	}})
	f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
		if index == 0 {
			emitNative(w, "r1", "completed", nativeCall("slow", "slow", `{}`))
		} else {
			emitNative(w, "r2", "completed", nativeText("foreground final"))
		}
	}, func(opts *testAgentOptions) { opts.ToolRegistry = registry })
	t.Cleanup(unblock)
	f.agent.output.dispatcher.RegisterPlatformSender("qq", f.platform)
	done := make(chan backgroundTestResult, 1)
	go func() {
		result, err := f.agent.RunBackground(context.Background(), background.RunRequest{Kind: background.KindCron, Name: "native", Platform: "qq", Actor: contextinfo.Actor{ID: "qq:1", Platform: "qq", PlatformUserID: "1", Role: contextinfo.RoleSuperadmin}, Prompt: "run", ToolListNames: []string{"slow"}, ModelProvider: "native", Model: "task-model"})
		done <- backgroundTestResult{result, err}
	}()
	awaitModelBarrier(t, started)
	rows, err := f.store.Sessions().List(t.Context(), storage.ListSessionsRequest{ActorID: "qq:1", Platform: "qq", IncludeSamePlatformBackground: true})
	if err != nil || len(rows) != 1 {
		t.Fatalf("background sessions=%+v %v", rows, err)
	}
	ctx := takeoverPrivateContext()
	if err := f.agent.HandleMessage(ctx, "/resume "+rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.HandleMessage(ctx, "takeover detail"); err != nil {
		t.Fatal(err)
	}
	unblock()
	result := awaitTakeover(t, done)
	requests := f.captured()
	if !result.TakenOver || result.Text != "foreground final" || len(requests) != 2 || requests[0].Model != "task-model" || requests[1].Model != "m" || requests[1].PreviousResponseID != "r1" {
		t.Fatalf("result=%+v requests=%+v", result, requests)
	}
	raw, _ := json.Marshal(requests[1].Input)
	if !strings.Contains(string(raw), "takeover detail") {
		t.Fatalf("pending missing: %s", raw)
	}
}

func TestResponsesItemDoneAndRefusalKeepNativeFactsSeparateFromDisplay(t *testing.T) {
	manager := hook.NewManager()
	_ = manager.Register(hook.Registration{Point: hook.PointLLMResponseReceived, Name: "display", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, error) {
		e.LLM.Text, e.LLM.SourceText = "visible", "changed source"
		return e, nil
	})})
	f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
		fmt.Fprint(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"opaque\",\"future\":true}}\n\n")
		emitNative(w, "r", "completed", `{"type":"reasoning"}`, `{"type":"message","content":[{"type":"refusal","refusal":"original refusal"}]}`)
	}, func(opts *testAgentOptions) { opts.HookManager = manager })
	if err := f.agent.HandleMessage(t.Context(), "input"); err != nil {
		t.Fatal(err)
	}
	row, err := f.agent.execution.sessions.Current(t.Context(), f.agent.Scope(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := f.store.Messages().ListBySession(t.Context(), row.ID)
	if err != nil || len(messages) != 2 || messages[1].Content != "original refusal" {
		t.Fatalf("native refusal display=%+v %v", messages, err)
	}
	checkpoint, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := f.store.Dialogues().GetExchange(t.Context(), checkpoint.ExchangeID)
	if err != nil || !strings.Contains(exchange.ItemsJSON, "opaque") || !strings.Contains(exchange.ItemsJSON, "original refusal") || strings.Contains(exchange.ResponseJSON, "changed source") {
		t.Fatalf("native facts=%+v %v", exchange, err)
	}
	if !strings.Contains(f.platform.out.String(), "visible") {
		t.Fatalf("display hook lost: %s", f.platform.out.String())
	}
}

type nativeTool struct {
	name string
	run  func(context.Context, tool.CallRequest) (*tool.Result, error)
}

type nativeRiskTool struct{ nativeTool }

func (t nativeRiskTool) Info() tool.Info { return tool.Info{Name: t.name, Risk: security.RiskHigh} }

func TestResponsesConfirmationRetainsOriginalCallAndCommitsResult(t *testing.T) {
	registry := tool.NewRegistry()
	count := 0
	_ = registry.Register(nativeRiskTool{nativeTool{name: "risk", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		count++
		return &tool.Result{Content: "confirmed result"}, nil
	}}})
	f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
		if index == 0 {
			emitNative(w, "r1", "completed", nativeCall("call", "risk", `{"original":true}`))
		} else {
			emitNative(w, "r2", "completed", nativeText("done"))
		}
	}, func(opts *testAgentOptions) {
		opts.ToolRegistry = registry
		opts.SecurityPolicy = security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}})
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- f.agent.HandleMessage(ctx, "@tool:risk run") }()
	deadline := time.Now().Add(3 * time.Second)
	var row *storage.Session
	var confirmation bool
	for time.Now().Before(deadline) {
		row, _ = f.agent.execution.sessions.Current(ctx, f.agent.Scope(ctx))
		if row != nil {
			if pending, ok := f.agent.execution.turns.PendingRiskConfirmation(row.ID); ok {
				if pending.Arguments != `{"original":true}` {
					t.Fatalf("changed confirmation=%+v", pending)
				}
				confirmation = true
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if !confirmation || count != 0 {
		t.Fatal("tool executed before confirmation or wait missing")
	}
	if err := f.agent.HandleMessage(ctx, "/confirm"); err != nil {
		t.Fatal(err)
	}
	awaitModelDone(t, done)
	requests := f.captured()
	if count != 1 || len(requests) != 2 || !strings.Contains(string(requests[1].Input[0]), "confirmed result") {
		t.Fatalf("count=%d requests=%+v", count, requests)
	}
}

type nativeAuditStore struct {
	storage.Store
	repository storage.DialogueRepository
}

func (s nativeAuditStore) Dialogues() storage.DialogueRepository { return s.repository }

type nativeAuditRepository struct {
	storage.DialogueRepository
	created chan string
}

func (r nativeAuditRepository) CreateExchange(ctx context.Context, row *storage.NativeExchange) error {
	if err := r.DialogueRepository.CreateExchange(ctx, row); err != nil {
		return err
	}
	r.created <- row.ID
	return nil
}

func TestResponsesCanceledAPIKeepsAuditWithoutAdvancingCheckpoint(t *testing.T) {
	started, release, unblock := modelBarrier(t)
	created := make(chan string, 1)
	f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
		fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\"}}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-release
	}, func(opts *testAgentOptions) {
		opts.Store = nativeAuditStore{Store: opts.Store, repository: nativeAuditRepository{DialogueRepository: opts.Store.Dialogues(), created: created}}
	})
	t.Cleanup(unblock)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- f.agent.HandleMessage(ctx, "input") }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("API did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("API cancellation did not finish")
	}
	unblock()
	id := <-created
	exchange, err := f.store.Dialogues().GetExchange(t.Context(), id)
	if err != nil || exchange.Status != "canceled" {
		t.Fatalf("canceled audit=%+v %v", exchange, err)
	}
	checkpoint, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), exchange.SessionID)
	if err != nil || checkpoint != nil {
		t.Fatalf("canceled checkpoint=%+v %v", checkpoint, err)
	}
}

func (t nativeTool) Name() string    { return t.name }
func (t nativeTool) Info() tool.Info { return tool.Info{Name: t.name, Risk: security.RiskLow} }
func (t nativeTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: t.name, Parameters: map[string]any{"type": "object"}}
}
func (t nativeTool) Call(ctx context.Context, r tool.CallRequest) (*tool.Result, error) {
	return t.run(ctx, r)
}

func TestResponsesUsesLocalToolsAndKeepsNativeCallIDs(t *testing.T) {
	var arguments string
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "echo", run: func(_ context.Context, r tool.CallRequest) (*tool.Result, error) {
		arguments = string(r.Arguments)
		return &tool.Result{Content: "actual result"}, nil
	}})
	f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
		if index == 0 {
			emitNative(w, "r1", "completed", `{"type":"reasoning","id":"reasoning","encrypted_content":"opaque","summary":[],"unknown":true}`, nativeCall("call1", "echo", `{"value":"original"}`))
		} else {
			emitNative(w, "r2", "completed", nativeText("finished"))
		}
	}, func(opts *testAgentOptions) { opts.ToolRegistry = registry })
	if err := f.agent.HandleMessage(t.Context(), "@tool:echo run"); err != nil {
		t.Fatal(err)
	}
	requests := f.captured()
	if arguments != `{"value":"original"}` || len(requests) != 2 || requests[1].PreviousResponseID != "r1" || len(requests[1].Input) != 1 || !strings.Contains(string(requests[1].Input[0]), `"call_id":"call1"`) || !strings.Contains(string(requests[1].Input[0]), "actual result") {
		t.Fatalf("arguments=%s requests=%+v", arguments, requests)
	}
	row, err := f.agent.execution.sessions.Current(t.Context(), f.agent.Scope(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := f.store.Messages().ListBySession(t.Context(), row.ID)
	if err != nil || len(messages) != 4 || messages[1].Role != storage.RoleAssistant || messages[2].ToolCallID != "call1" {
		t.Fatalf("tool display=%+v %v", messages, err)
	}
	checkpoint, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := f.store.Dialogues().GetExchange(t.Context(), checkpoint.ExchangeID)
	if err != nil || exchange.PreviousCheckpointID == "" {
		t.Fatalf("exchange=%+v %v", exchange, err)
	}
}

func TestResponsesRejectsToolArgumentHookButKeepsDisplayAndResultHooks(t *testing.T) {
	for _, rewrite := range []bool{false, true} {
		t.Run(fmt.Sprint(rewrite), func(t *testing.T) {
			executed := false
			registry := tool.NewRegistry()
			_ = registry.Register(nativeTool{name: "echo", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
				executed = true
				return &tool.Result{Content: "original result"}, nil
			}})
			manager := hook.NewManager()
			if rewrite {
				_ = manager.Register(hook.Registration{Point: hook.PointToolCallPrepared, Name: "rewrite", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, error) {
					e.Tool.Arguments = `{"changed":true}`
					return e, nil
				})})
			} else {
				_ = manager.Register(hook.Registration{Point: hook.PointToolCallCompleted, Name: "result", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, error) {
					e.Message.Segments = llm.TextSegments("changed result")
					return e, nil
				})})
			}
			_ = manager.Register(hook.Registration{Point: hook.PointLLMResponseReceived, Name: "display", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, error) {
				if len(e.LLM.ToolCalls) == 0 {
					e.LLM.Text = "visible reply"
				}
				return e, nil
			})})
			f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
				if index == 0 {
					emitNative(w, "r1", "completed", nativeCall("call", "echo", `{}`))
				} else {
					emitNative(w, "r2", "completed", nativeText("original reply"))
				}
			}, func(opts *testAgentOptions) { opts.ToolRegistry = registry; opts.HookManager = manager })
			if err := f.agent.HandleMessage(t.Context(), "@tool:echo run"); err != nil {
				t.Fatal(err)
			}
			requests := f.captured()
			if len(requests) != 2 || executed == rewrite {
				t.Fatalf("executed=%v requests=%+v", executed, requests)
			}
			want := "changed result"
			if rewrite {
				want = "read-only"
			}
			if !strings.Contains(string(requests[1].Input[0]), want) || !strings.Contains(f.platform.out.String(), "visible reply") {
				t.Fatalf("hook result requests=%+v output=%s", requests, f.platform.out.String())
			}
		})
	}
}

func TestResponsesIncompleteAndEOFDoNotAdvanceCheckpoint(t *testing.T) {
	for _, kind := range []string{"incomplete", "eof"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
				if kind == "incomplete" {
					emitNative(w, "r", "incomplete", nativeText("partial"))
				} else {
					fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
				}
			})
			if err := f.agent.HandleMessage(t.Context(), "input"); err == nil {
				t.Fatal("partial response completed")
			}
			row, err := f.agent.execution.sessions.Current(t.Context(), f.agent.Scope(t.Context()))
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), row.ID)
			if err != nil || checkpoint != nil {
				t.Fatalf("partial checkpoint=%+v %v", checkpoint, err)
			}
			messages, err := f.store.Messages().ListBySession(t.Context(), row.ID)
			if err != nil || len(messages) != 1 {
				t.Fatalf("partial display=%+v %v", messages, err)
			}
		})
	}
}

func TestResponsesStoppedToolRoundKeepsCompletedResultsWithoutReexecution(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var first, second int
	registry := tool.NewRegistry()
	_ = registry.Register(nativeTool{name: "one", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
		first++
		return &tool.Result{Content: "saved first"}, nil
	}})
	_ = registry.Register(nativeTool{name: "two", run: func(ctx context.Context, _ tool.CallRequest) (*tool.Result, error) {
		second++
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &tool.Result{Content: "late"}, nil
		}
	}})
	f := newNativeFixture(t, func(index int, _ nativeTestRequest, w http.ResponseWriter) {
		if index == 0 {
			emitNative(w, "r1", "completed", nativeCall("one", "one", `{}`), nativeCall("two", "two", `{}`))
		} else {
			emitNative(w, "r2", "completed", nativeText("continued"))
		}
	}, func(opts *testAgentOptions) { opts.ToolRegistry = registry })
	done := make(chan error, 1)
	go func() { done <- f.agent.HandleMessage(context.Background(), "@tool:one @tool:two run") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("second tool did not start")
	}
	if err := f.agent.HandleMessage(t.Context(), "/stop 1"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not finish")
	}
	if err := f.agent.HandleMessage(t.Context(), "continue"); err != nil {
		t.Fatal(err)
	}
	requests := f.captured()
	if first != 1 || second != 1 || len(requests) != 2 || requests[1].PreviousResponseID != "r1" || len(requests[1].Input) != 3 {
		t.Fatalf("replayed tools %d/%d requests=%+v", first, second, requests)
	}
	if !strings.Contains(string(requests[1].Input[0]), "saved first") || !strings.Contains(string(requests[1].Input[1]), "outcome is unknown") {
		t.Fatalf("lost stopped results: %+v", requests[1])
	}
}

func TestResponsesEmptyReplyAndBufferedSendFailureKeepLocalCheckpoint(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
				if empty {
					emitNative(w, "r", "completed", `{"type":"unknown","keep":true}`)
				} else {
					emitNative(w, "r", "completed", nativeText("reply"))
				}
			})
			failure := errors.New("send failed")
			ctx := platform.WithMessageContext(t.Context(), platform.MessageContext{BufferAssistantOutput: true, Sender: mediaSendFunc(func([]delivery.Output) (delivery.Receipt, error) { return delivery.Receipt{}, failure })})
			if err := f.agent.HandleMessage(ctx, "input"); !errors.Is(err, failure) {
				t.Fatalf("send error=%v", err)
			}
			row, err := f.agent.execution.sessions.Current(ctx, f.agent.Scope(ctx))
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, err := f.store.Dialogues().CurrentCheckpoint(ctx, row.ID)
			if err != nil || checkpoint == nil || checkpoint.ResponseID != "r" {
				t.Fatalf("lost local checkpoint=%+v %v", checkpoint, err)
			}
			if empty && checkpoint.MessageID != "" {
				t.Fatalf("invented empty display message: %+v", checkpoint)
			}
		})
	}
}
