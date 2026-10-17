package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/command"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/llm/httpclient"
	"elbot/internal/llm/responses"
	"elbot/internal/notification/rules"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
)

func TestModelHeaderTimeoutNotifiesOnce(t *testing.T) {
	for _, protocol := range []string{"chat", "response"} {
		for _, entry := range []string{"input", "message"} {
			t.Run(protocol+"/"+entry, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var requests, retries atomic.Int32
					const endpoint = "http://provider.test"
					// net.Pipe keeps HTTP parsing and transport timeouts inside the
					// virtual clock, without depending on OS network scheduling.
					transport := &http.Transport{DisableKeepAlives: true, ResponseHeaderTimeout: time.Second, DialContext: func(context.Context, string, string) (net.Conn, error) {
						client, peer := net.Pipe()
						go func() {
							defer peer.Close()
							req, err := http.ReadRequest(bufio.NewReader(peer))
							if err != nil {
								t.Error(err)
								return
							}
							_, _ = io.Copy(io.Discard, req.Body)
							_ = req.Body.Close()
							if req.URL.Path == "/models" {
								body := `{"data":[{"id":"model"}]}`
								response := &http.Response{StatusCode: http.StatusOK, ProtoMajor: 1, ProtoMinor: 1, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Close: true}
								_ = response.Write(peer)
								return
							}
							requests.Add(1)
							_, _ = io.Copy(io.Discard, peer)
						}()
						return client, nil
					}}
					defer transport.CloseIdleConnections()
					opts := httpclient.Options{HTTPClient: &http.Client{Transport: transport}, FirstChunkTimeout: time.Second, MaxRetries: 3, RetryInitialDelay: time.Second}

					var client llm.Client
					var err error
					if protocol == "chat" {
						client, err = chatcompletions.New(endpoint, "", nil, nil, opts)
					} else {
						client, err = responses.New(endpoint, "", nil, nil, opts)
					}
					if err != nil {
						t.Fatal(err)
					}
					p := &fakePlatform{}
					a := newTestAgent(t, p, client, "model", config.ProviderConfig{APIMode: protocol, BaseURL: endpoint}, newTestStore(t), func(opts *testAgentOptions) {
						opts.SessionConfig.NamingConfig.TriggerStep = 100
					})
					logs, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
						if record.EventName == "model_retry" {
							retries.Add(1)
						}
						return nil
					}, signal.ConnectOptions{})
					if err != nil {
						t.Fatal(err)
					}
					defer logs.Disconnect()
					if entry == "input" {
						err = a.input.HandleInput(t.Context(), "hello")
					} else {
						err = a.HandleMessage(t.Context(), "hello")
					}
					if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timeout awaiting response headers") {
						t.Fatalf("expected transport deadline error, got %v", err)
					}
					if requests.Load() != 4 || retries.Load() != 3 {
						t.Fatalf("requests=%d retries=%d", requests.Load(), retries.Load())
					}
					output := awaitNotificationText(t, &p.out, "对话处理失败：")
					if strings.Count(output, "LLM 请求失败，正在重试") != 3 || strings.Count(output, "对话处理失败：") != 1 || !strings.Contains(output, "timeout awaiting response headers") {
						t.Fatalf("incorrect terminal notification: %q", output)
					}
				})
			})
		}
	}
}

func TestModelStreamFailureNotifiesOnce(t *testing.T) {
	for _, protocol := range []string{"chat", "response"} {
		t.Run(protocol, func(t *testing.T) {
			var a *agentFixture
			var p *fakePlatform
			if protocol == "chat" {
				p = &fakePlatform{}
				a = newTestAgent(t, p, &fakeLLM{replies: []string{"__ERR__"}}, "model", config.ProviderConfig{}, newTestStore(t))
			} else {
				f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
					fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"message\":\"test stream failure\",\"code\":\"server_error\"}\n\n")
				})
				a, p = f.agent, f.platform
			}
			if err := a.input.HandleInput(t.Context(), "hello"); err == nil {
				t.Fatal("stream failure was swallowed")
			}
			output := awaitNotificationText(t, &p.out, "对话处理失败：")
			if strings.Count(output, "对话处理失败：") != 1 {
				t.Fatalf("incorrect stream notification: %q", output)
			}
		})
	}
}

func TestCommandFailureReportsOnceWithoutFaultLog(t *testing.T) {
	want := errors.New("command storage unavailable")
	p := &fakePlatform{}
	a := newTestAgent(t, p, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t))
	failures := observeFailureLogs(t)
	e := a.message.commands.(*CommandExecutor)
	if err := e.router.Register(command.NewFunc(command.Info{Name: "failure"}, func(context.Context, command.Request) (*command.Result, error) {
		return nil, want
	})); err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithActor(t.Context(), a.identity.Actor(t.Context()))
	handled, err := e.Handle(ctx, "/failure")
	if !handled || !errors.Is(err, want) {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	output := awaitNotificationText(t, &p.out, "命令 /failure 执行失败：")
	if strings.Count(output, "命令 /failure 执行失败：") != 1 || !strings.Contains(output, want.Error()) {
		t.Fatalf("incorrect command notification: %q", output)
	}
	if failures.Load() != 0 {
		t.Fatalf("fault logs=%d", failures.Load())
	}
}

func TestPreparationErrorReportedOnceByDialogue(t *testing.T) {
	want := errors.New("prompt source unavailable")
	p := &fakePlatform{}
	a := newTestAgent(t, p, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t))
	failures := observeFailureLogs(t)
	testChatRoute(a).PromptBuilder.System = dialogue.NewSystemPromptManager(preparationPromptSource(func(context.Context, dialogue.SystemPromptRequest) ([]dialogue.SystemPromptPart, error) {
		return nil, want
	}))
	if err := a.input.HandleInput(t.Context(), "hello"); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	output := awaitNotificationText(t, &p.out, "对话处理失败：")
	if strings.Count(output, "对话处理失败：") != 1 || !strings.Contains(output, want.Error()) {
		t.Fatalf("notification=%q", output)
	}
	if failures.Load() != 1 {
		t.Fatalf("fault logs=%d", failures.Load())
	}
}

type notificationFailingDialogues struct {
	storage.DialogueRepository
	err error
}

func (r notificationFailingDialogues) Commit(context.Context, storage.DialogueCommit) (events.LogResult, error) {
	return events.LogResult{ResultStatus: events.ResultFailed}, r.err
}

func TestPersistenceFailureReturnsAtStoreAndNotifiesAtDialogue(t *testing.T) {
	want := errors.New("dialogue disk full")
	p := &fakePlatform{}
	a := newTestAgent(t, p, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t))
	_, row, err := a.execution.ResolveInput(t.Context(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	messages := testChatRoute(a).Messages
	messages.Dialogues = notificationFailingDialogues{DialogueRepository: messages.Dialogues, err: want}
	_, err = messages.Append(t.Context(), &storage.Message{SessionID: row.ID, Role: storage.RoleUser, Content: "hello"}, "append_user_message")
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	assertNoFailureNotification(t, p)
	if err := a.HandleMessage(t.Context(), "hello"); !errors.Is(err, want) {
		t.Fatalf("dialogue err=%v", err)
	}
	output := awaitNotificationText(t, &p.out, "对话处理失败：")
	if strings.Count(output, "对话处理失败：") != 1 || !strings.Contains(output, want.Error()) {
		t.Fatalf("notification=%q", output)
	}
}

func TestMessageEntryDoesNotRepeatModelErrorHook(t *testing.T) {
	manager := newTestHookManager()

	var calls atomic.Int32
	manager.Register(hook.Registration{Point: hook.PointErrorOccurred, Name: "failure.observer", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, events.LogResult, error) {
		calls.Add(1)
		return event, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	})})
	p := &fakePlatform{}
	a := newTestAgent(t, p, &fakeLLM{replies: []string{"__ERR__"}}, "model", config.ProviderConfig{}, newTestStore(t), func(opts *testAgentOptions) { opts.HookManager = manager })
	if err := a.HandleMessage(t.Context(), "hello"); err == nil {
		t.Fatal("model failure was swallowed")
	}
	awaitNotificationText(t, &p.out, "对话处理失败：")
	if calls.Load() != 1 {
		t.Fatalf("same error reclassified and reported %d times", calls.Load())
	}
}

func TestHookErrorTypesDoNotSuppressBusinessFailures(t *testing.T) {
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(want.Error(), func(t *testing.T) {
			manager := newTestHookManager()

			manager.Register(hook.Registration{Point: hook.PointAgentInputPrepared, Name: "failure.hook", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, events.LogResult, error) {
				return event, events.LogResult{ResultStatus: events.ResultFailed}, want
			})})
			p := &fakePlatform{}
			a := newTestAgent(t, p, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t), func(opts *testAgentOptions) { opts.HookManager = manager })
			err := a.HandleMessage(t.Context(), "hello")
			if !errors.Is(err, want) {
				t.Fatalf("err=%v", err)
			}
			output := awaitNotificationText(t, &p.out, "对话处理失败：")
			if strings.Count(output, want.Error()) != 1 {
				t.Fatalf("notification=%q", output)
			}
		})
	}
}

func TestErrorHookFailureDoesNotNotifyOrRecurse(t *testing.T) {
	manager := newTestHookManager()

	var calls atomic.Int32
	manager.Register(hook.Registration{Point: hook.PointErrorOccurred, Name: "failure.error_hook", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, events.LogResult, error) {
		calls.Add(1)
		return event, events.LogResult{ResultStatus: events.ResultFailed}, errors.New("error hook failed")
	})})
	p := &fakePlatform{}
	a := newTestAgent(t, p, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t), func(opts *testAgentOptions) { opts.HookManager = manager })
	a.hooks.NotifyError(t.Context(), hook.Event{Point: hook.PointLLMResponseReceived}, errors.New("upstream failed"))
	assertNoFailureNotification(t, p)
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestFailureNotificationDeliveryDoesNotReportAnotherFailure(t *testing.T) {
	var sends atomic.Int32
	a, _ := commandNotificationFixture(t, func(_ context.Context, n events.Notification) error {
		if n.Text == "barrier" {
			return nil
		}
		sends.Add(1)
		return errors.New("platform unavailable")
	})
	foregroundTurnOutput{sender: a.output}.Notify(t.Context(), slog.LevelError, "source failure")
	if _, err := events.NotificationRequested.Emit(t.Context(), events.Notification{Text: "barrier"}); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 1 {
		t.Fatalf("notification delivery attempted %d times", sends.Load())
	}
}

func assertNoFailureNotification(t *testing.T, p *fakePlatform) {
	t.Helper()
	if _, err := events.NotificationRequested.Emit(t.Context(), events.Notification{Text: "barrier"}); err != nil {
		t.Fatal(err)
	}
	if output := strings.TrimSpace(p.out.String()); output != "barrier" {
		t.Fatalf("unexpected notification: %q", output)
	}
}

func observeFailureLogs(t *testing.T) *atomic.Int32 {
	t.Helper()
	count := new(atomic.Int32)
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.Level >= slog.LevelError || record.ResultStatus == events.ResultFailed {
			count.Add(1)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Disconnect() })
	return count
}

func TestContextPreparationNotifiesOnceAtCore(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	a := newTestAgent(t, p, &fakeLLM{}, "model", config.ProviderConfig{}, store)
	want := errors.New("context read denied")
	a.dialogue.Preparer.Contexts = contextmgr.New(contextmgr.Options{Store: failureToolStateStore{Store: store, err: want}})
	if err := a.HandleMessage(t.Context(), "hello"); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	output := awaitNotificationText(t, &p.out, "对话处理失败：")
	if strings.Count(output, "对话处理失败：") != 1 || !strings.Contains(output, want.Error()) {
		t.Fatalf("output=%q", output)
	}
}

type failureToolStateStore struct {
	storage.Store
	row *storage.Session
	err error
}
type failureToolStateSessions struct {
	storage.SessionRepository
	row *storage.Session
	err error
}

func (s failureToolStateStore) Sessions() storage.SessionRepository {
	return failureToolStateSessions{SessionRepository: s.Store.Sessions(), row: s.row, err: s.err}
}
func (s failureToolStateSessions) Get(context.Context, string) (*storage.Session, error) {
	return s.row, s.err
}

func TestToolPreparationReturnsAtCoreAndNotifiesAtDialogue(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	a := newTestAgent(t, p, &fakeLLM{}, "model", config.ProviderConfig{}, store)
	ctx, row, err := a.execution.ResolveInput(t.Context(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("tool state read unavailable")
	tools := a.dialogue.Preparer.Tools
	tools.State = toolrun.NewStateService(failureToolStateStore{Store: store, err: want})
	if _, err := tools.Schemas(ctx, row); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	assertNoFailureNotification(t, p)
	if err := a.HandleMessage(ctx, "hello"); !errors.Is(err, want) {
		t.Fatalf("dialogue err=%v", err)
	}
	output := awaitNotificationText(t, &p.out, "对话处理失败：")
	if strings.Count(output, "对话处理失败：") != 1 || !strings.Contains(output, want.Error()) {
		t.Fatalf("output=%q", output)
	}
}

func TestFailureNotificationKeepsOriginAfterSessionSwitch(t *testing.T) {
	seen := make(chan events.Notification, 1)
	a, _ := commandNotificationFixture(t, func(ctx context.Context, n events.Notification) error {
		if ctx.Err() != nil {
			t.Error("failure notification inherited request cancellation")
		}
		if _, ok := session.BindingFromContext(ctx); ok {
			t.Error("failure notification inherited obsolete session binding")
		}
		seen <- n
		return nil
	})
	ctx := t.Context()
	scope := a.Scope(ctx)
	if _, err := a.execution.sessions.Create(ctx, scope, session.CreateRequest{Title: "first"}); err != nil {
		t.Fatal(err)
	}
	_, binding, err := a.execution.sessions.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	origin := contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ScopeID: "original", ConversationID: "original"}}
	ctx, cancel := context.WithCancel(session.WithBinding(contextinfo.WithConversation(ctx, origin), binding))
	if _, err := a.execution.sessions.Create(t.Context(), scope, session.CreateRequest{Title: "second"}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if binding.Valid() {
		t.Fatal("test did not invalidate original binding")
	}
	foregroundTurnOutput{sender: a.output}.Notify(context.WithoutCancel(ctx), slog.LevelError, "late failure")
	select {
	case notice := <-seen:
		if notice.Origin == nil || notice.Origin.Source != origin.Source || notice.Text != "late failure" {
			t.Fatalf("notification=%+v", notice)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal notification was dropped")
	}
}

func TestTurnDeadlineNotifiesFromLifecycle(t *testing.T) {
	p := &fakePlatform{}
	a := newTestAgent(t, p, &fakeLLM{chatBlocks: []fakeLLMBlock{{started: make(chan struct{}), release: make(chan struct{})}}}, "model", config.ProviderConfig{}, newTestStore(t))
	a.execution.responseTimeout = 20 * time.Millisecond
	if err := a.input.HandleInput(t.Context(), "hello"); err != nil {
		t.Fatalf("lifecycle timeout escaped as another failure: %v", err)
	}
	output := awaitNotificationText(t, &p.out, rules.TurnTimeout)
	if strings.Count(output, rules.TurnTimeout) != 1 {
		t.Fatalf("notification=%q", output)
	}
}

func TestDialogueSynchronousHookFailuresHaveOneTerminalReport(t *testing.T) {
	for _, protocol := range []string{"chat", "response"} {
		for _, point := range []hook.Point{hook.PointLLMTurnPrepared, hook.PointLLMRequestPrepared, hook.PointLLMResponseReceived, hook.PointAgentTurnOutputPrepared, hook.PointAgentOutputPrepared} {
			t.Run(protocol+"/"+string(point), func(t *testing.T) {
				want := errors.New("injected synchronous hook failure")
				manager := newTestHookManager()
				manager.Register(hook.Registration{Point: point, Name: "failure.boundary", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, events.LogResult, error) {
					return event, events.LogResult{ResultStatus: events.ResultFailed}, want
				})})
				configure := func(opts *testAgentOptions) { opts.HookManager = manager }
				var a *agentFixture
				var p *fakePlatform
				var calls func() int
				if protocol == "response" {
					f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
						emitNative(w, "response", "completed", nativeText("answer"))
					}, configure)
					a, p, calls = f.agent, f.platform, func() int { return len(f.captured()) }
				} else {
					p = &fakePlatform{}
					client := &fakeLLM{replies: []string{"answer"}}
					a = newTestAgent(t, p, client, "model", config.ProviderConfig{}, newTestStore(t), configure)
					calls = func() int { return len(client.chatRequests()) }
				}
				var failures atomic.Int32
				connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
					if record.EventName == "dialogue_failed" {
						failures.Add(1)
					}
					return nil
				}, signal.ConnectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				defer connection.Disconnect()
				if err := a.HandleMessage(t.Context(), "hello"); !errors.Is(err, want) {
					t.Fatalf("error=%v", err)
				}
				output := awaitNotificationText(t, &p.out, "对话处理失败：")
				if failures.Load() != 1 || strings.Count(output, "对话处理失败：") != 1 || strings.Contains(output, "Hook 执行失败") || !strings.Contains(output, want.Error()) {
					t.Fatalf("failures=%d notification=%q", failures.Load(), output)
				}
				wantCalls := 1
				if point == hook.PointLLMTurnPrepared || point == hook.PointLLMRequestPrepared {
					wantCalls = 0
				}
				if calls() != wantCalls {
					t.Fatalf("later model call was not stopped: calls=%d want=%d", calls(), wantCalls)
				}
			})
		}
	}
}
