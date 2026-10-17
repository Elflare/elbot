package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/modelmgr"
	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// Interleave a competing admission after AcceptInput has already observed idle.
// This uses an existing dependency boundary; production code needs no test hook.
type admissionRaceDialogue struct {
	ExecutionDialogue
	check func()
}

func (d admissionRaceDialogue) CheckSelection(selection modelmgr.Selection) error {
	d.check()
	return d.ExecutionDialogue.CheckSelection(selection)
}

type admissionRaceRows struct {
	storage.SessionRepository
	beforeGet func(context.Context)
}

func (r admissionRaceRows) Get(ctx context.Context, id string) (*storage.Session, error) {
	r.beforeGet(ctx)
	return r.SessionRepository.Get(ctx, id)
}

func TestInputStartConflictRedispatchesPreparedMessage(t *testing.T) {
	for _, input := range []string{"/new", "different input"} {
		for _, phase := range []turn.Phase{turn.PhaseLLM, turn.PhaseTool, turn.PhaseAwaitAppendConfirm, turn.PhaseIdle} {
			t.Run(input+"/"+string(phase), func(t *testing.T) {
				p := &fakePlatform{}
				model := &fakeLLM{replies: []string{"normal reply"}}
				hooks := newTestHookManager()

				prepared := 0
				hooks.Register(hook.Registration{Point: hook.PointAgentInputPrepared, Name: "count prepared input", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, events.LogResult, error) {
					prepared++
					return e, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
				})})
				a := newTestAgent(t, p, model, "model", config.ProviderConfig{}, newTestStore(t), func(opts *testAgentOptions) {
					opts.CommandPrefixes = []string{"-"}
					opts.HookManager = hooks
				})
				ctx, row, err := a.execution.ResolveInput(context.Background(), "/new")
				if err != nil {
					t.Fatal(err)
				}
				competing := turn.NewExecution("competing")
				injected, readmitted := false, false
				a.execution.dialogue = admissionRaceDialogue{ExecutionDialogue: a.execution.dialogue, check: func() {
					if injected {
						return
					}
					injected = true
					if !a.execution.turns.StartExecution(row.ID, turn.Input{Text: "/new"}, competing, "competing-attempt") {
						t.Fatal("competing execution did not start")
					}
					if phase == turn.PhaseTool {
						a.execution.turns.StartToolPhase(row.ID, "competing-attempt")
					} else if phase == turn.PhaseAwaitAppendConfirm {
						a.execution.turns.InterruptLLMInput(row.ID, turn.Input{Text: "already pending"})
					}
				}}
				a.execution.sessionRows = admissionRaceRows{SessionRepository: a.execution.sessionRows, beforeGet: func(ctx context.Context) {
					// Reentry uses the original prepared context, not the failed execution.
					if injected && turn.ExecutionFromContext(ctx) == nil {
						readmitted = true
						if phase == turn.PhaseIdle {
							a.execution.turns.CompleteLLMInput(row.ID, "competing-attempt")
							competing.Finish(nil)
						}
					}
				}}
				if err := a.HandleMessage(ctx, input); err != nil {
					t.Fatalf("ordinary input failed: %v", err)
				}
				if !injected || !readmitted || prepared != 1 {
					t.Fatalf("injected=%v readmitted=%v prepared=%d", injected, readmitted, prepared)
				}
				output := p.out.String()
				if strings.Contains(output, "请求失败") || strings.Contains(output, "暂不支持切换") {
					t.Fatalf("false failure: %q", output)
				}
				if phase == turn.PhaseIdle {
					if !strings.Contains(output, "normal reply") {
						t.Fatalf("input did not start after competitor finished: %q", output)
					}
					return
				}
				if len(model.requests) != 0 {
					t.Fatal("losing input called the model before confirmation")
				}
				var merged turn.Input
				if phase == turn.PhaseTool {
					merged = a.execution.turns.DrainMergedInput(row.ID, "competing-attempt")
				} else {
					var ok bool
					merged, _, ok = a.execution.turns.ResumeAppend(row.ID)
					if !ok {
						t.Fatal("input did not enter append confirmation")
					}
				}
				if !strings.Contains(merged.Text, input) {
					t.Fatalf("input lost: %+v", merged)
				}
				if input == "/new" && phase != turn.PhaseTool && strings.Count(merged.Text, "/new") != 2 {
					t.Fatalf("identical inputs were not both retained: %+v", merged)
				}
			})
		}
	}
}

func TestInputExecutionFailureStillReported(t *testing.T) {
	p := &fakePlatform{}
	a := newTestAgent(t, p, &fakeLLM{replies: []string{"__ERR__"}}, "model", config.ProviderConfig{}, newTestStore(t))
	err := a.HandleMessage(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "fake stream error") {
		t.Fatalf("execution error was swallowed: %v", err)
	}
	output := awaitNotificationText(t, &p.out, "对话处理失败：")
	if !strings.Contains(output, "fake stream error") || strings.Contains(output, "暂不支持切换") {
		t.Fatalf("wrong failure notification: %q", output)
	}
}

func TestSessionSwitchBusyIsWarnNotification(t *testing.T) {
	for _, path := range []string{"command", "session"} {
		t.Run(path, func(t *testing.T) {
			p := &fakePlatform{}
			a := newTestAgent(t, p, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t), func(opts *testAgentOptions) {
				opts.CommandPrefixes = []string{"-"}
			})
			ctx := context.Background()
			target, err := a.execution.sessions.Create(ctx, a.identity.Scope(ctx), session.CreateRequest{Title: "target"})
			if err != nil {
				t.Fatal(err)
			}
			current, err := a.execution.sessions.Create(ctx, a.identity.Scope(ctx), session.CreateRequest{Title: "current"})
			if err != nil {
				t.Fatal(err)
			}
			a.execution.turns.StartLLM(current.ID, "input")
			hub, _ := fixtureNotificationHubs.Load(t)
			if err := hub.(*fixtureNotificationHub).manager.Close(ctx); err != nil {
				t.Fatal(err)
			}
			var notices []events.Notification
			connection, err := events.NotificationRequested.Observe(func(_ context.Context, notice events.Notification) error {
				notices = append(notices, notice)
				p.out.WriteString(notice.Text)
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			text := "-new"
			if path == "session" {
				ctx = platform.WithMessageContext(ctx, platform.MessageContext{ResumeSessionID: target.ID})
				text = "ordinary input"
			}
			if err := a.HandleMessage(ctx, text); err != nil {
				t.Fatalf("expected busy condition escaped to platform error log: %v", err)
			}
			if len(notices) != 1 || notices[0].Level != slog.LevelWarn || notices[0].Text != session.ErrSessionBusy.Error() {
				t.Fatalf("notifications: %+v", notices)
			}
			if p.out.String() != session.ErrSessionBusy.Error() {
				t.Fatalf("notification output: %q", p.out.String())
			}
		})
	}
}

func TestRunAttemptConflictHasExecutionMeaning(t *testing.T) {
	f := newExecutionFixture(t, &fakeLLM{}, newTestStore(t))
	ctx, row, err := f.execution.ResolveInput(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	f.opts.Turns.StartExecution(row.ID, turn.Input{Text: "first"}, turn.NewExecution("first"), "attempt")
	err = f.execution.Run(ctx, row, "hello", f.out)
	if !errors.Is(err, errExecutionConflict) || errors.Is(err, session.ErrSessionBusy) || strings.Contains(err.Error(), "切换") {
		t.Fatalf("wrong admission error: %v", err)
	}
	if f.opts.Turns.Execution(row.ID).ID != "first" {
		t.Fatal("losing admission replaced the active execution")
	}
}

func TestInputContinuationKeepsAdmissionOwnership(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "reserved", true: "stopped"}[stopped], func(t *testing.T) {
			model := &fakeLLM{replies: []string{"first reply", "second reply"}}
			f := newExecutionFixture(t, model, newTestStore(t))
			ctx, row, err := f.execution.ResolveInput(context.Background(), "original")
			if err != nil {
				t.Fatal(err)
			}
			attempts := 0
			f.on(t, hook.PointLLMResponseReceived, func(e hook.Event) hook.Event {
				if attempts == 1 {
					f.opts.Turns.StartToolPhase(row.ID)
					f.opts.Turns.AppendPendingInput(row.ID, turn.Input{Text: "follow-up"})
				}
				return e
			})
			f.execution.dialogue = admissionRaceDialogue{ExecutionDialogue: f.execution.dialogue, check: func() {
				attempts++
				if attempts != 2 {
					return
				}
				if stopped {
					f.opts.Turns.StopSession(row.ID)
				}
				started := f.opts.Turns.StartExecution(row.ID, turn.Input{Text: "competitor"}, turn.NewExecution("competitor"), "competitor-attempt")
				if started != stopped {
					t.Fatalf("competing start=%v, stopped=%v", started, stopped)
				}
			}}
			_, err = f.execution.AcceptInput(ctx, row, "original")
			if stopped {
				if !errors.Is(err, context.Canceled) || len(model.requests) != 1 || f.opts.Turns.Snapshot(row.ID).Phase != turn.PhaseLLM {
					t.Fatalf("stopped continuation was replayed: err=%v model calls=%d", err, len(model.requests))
				}
			} else if err != nil || len(model.requests) != 2 {
				t.Fatalf("reserved continuation failed: err=%v model calls=%d", err, len(model.requests))
			}
			if attempts != 2 {
				t.Fatalf("attempts=%d, want 2", attempts)
			}
		})
	}
}

type admissionSendingPlatform struct {
	fakePlatform
	started chan struct{}
	release chan struct{}
}

func (p *admissionSendingPlatform) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	if delivery.FallbackOutput(outputs).Text == "normal reply" {
		close(p.started)
		<-p.release
	}
	return p.fakePlatform.SendChat(ctx, outputs)
}

func TestInputDuringPlatformSendUsesAppendConfirmation(t *testing.T) {
	p := &admissionSendingPlatform{started: make(chan struct{}), release: make(chan struct{})}
	a := newTestAgent(t, p, &fakeLLM{replies: []string{"normal reply"}}, "model", config.ProviderConfig{}, newTestStore(t), func(opts *testAgentOptions) {
		opts.CommandPrefixes = []string{"-"}
	})
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(p.release) }) })
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{BufferAssistantOutput: true, Sender: p})
	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "first input") }()
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("platform send did not start")
	}
	if err := a.HandleMessage(ctx, "/new"); err != nil {
		t.Fatalf("input during send: %v", err)
	}
	if !strings.Contains(p.out.String(), "是否追加这条消息") {
		t.Fatalf("missing append confirmation: %q", p.out.String())
	}
	unblock.Do(func() { close(p.release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("original input: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original input did not finish")
	}
	if strings.Contains(p.out.String(), "请求失败") || strings.Contains(p.out.String(), "暂不支持切换") {
		t.Fatalf("false failure during send: %q", p.out.String())
	}
}
