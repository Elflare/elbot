package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextinfo"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/request"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
)

type preparationPromptSource func(context.Context, dialogue.SystemPromptRequest) ([]dialogue.SystemPromptPart, error)

func (f preparationPromptSource) Parts(ctx context.Context, req dialogue.SystemPromptRequest) ([]dialogue.SystemPromptPart, error) {
	return f(ctx, req)
}

func TestTurnPreparationUsesRequestContext(t *testing.T) {
	for _, stage := range []string{"prompt", "hook"} {
		for _, reason := range []string{"cancel", "timeout"} {
			t.Run(stage+"/"+reason, func(t *testing.T) {
				model := &fakeLLM{replies: []string{"unexpected"}}
				f := newExecutionFixture(t, model, newTestStore(t))
				f.hooks.SetObserver(f.chat.Preparer.Hooks.(*hookBridge).ObserveRun)
				ctx, reqCtx, in, _ := f.begin(t)
				wantErr := context.Canceled
				if reason == "timeout" {
					var cancel context.CancelFunc
					reqCtx, cancel = context.WithTimeout(reqCtx, 20*time.Millisecond)
					defer cancel()
					wantErr = context.DeadlineExceeded
				}
				var observedID, parentID string
				var observedErr error
				observe := func(preparationCtx context.Context) {
					observedID = contextinfo.RootRequestIDFromContext(preparationCtx)
					if stage == "hook" {
						for _, active := range f.opts.Requests.ListBySession(in.Session.ID) {
							if active.Kind == request.KindHook {
								parentID = active.ParentID
							}
						}
					}
					if reason == "cancel" {
						f.opts.Requests.Cancel(in.RequestID)
					} else {
						select {
						case <-preparationCtx.Done():
						case <-time.After(time.Second):
							t.Error("preparation did not receive the request deadline")
						}
					}
					observedErr = preparationCtx.Err()
				}
				if stage == "prompt" {
					f.chat.PromptBuilder.System = dialogue.NewSystemPromptManager(preparationPromptSource(func(pctx context.Context, _ dialogue.SystemPromptRequest) ([]dialogue.SystemPromptPart, error) {
						observe(pctx)
						// A provider may return success despite cancellation; preparation
						// must still stop before persisting the new user message.
						return []dialogue.SystemPromptPart{{Content: "test"}}, nil
					}))
				} else if err := f.hooks.Register(hook.Registration{Point: hook.PointLLMTurnPrepared, Name: "preparation.cancel", Match: hook.Always(), Handler: hook.HandlerFunc(func(hctx context.Context, event hook.Event) (hook.Event, error) {
					observe(hctx)
					return event, nil
				})}); err != nil {
					t.Fatal(err)
				}
				result := f.runner.RunTurn(ctx, reqCtx, in, f.out)
				if observedID != in.RequestID || !errors.Is(observedErr, wantErr) {
					t.Fatalf("request ownership: id=%q want=%q err=%v want=%v", observedID, in.RequestID, observedErr, wantErr)
				}
				if stage == "hook" && parentID != in.RequestID {
					t.Fatalf("hook parent=%q want=%q", parentID, in.RequestID)
				}
				if result.Outcome != dialogue.Canceled || !result.QuietCancellation || !errors.Is(result.Err, wantErr) {
					t.Fatalf("result=%+v want canceled with %v", result, wantErr)
				}
				messages, err := f.opts.Store.Messages().ListBySession(context.Background(), in.Session.ID)
				if err != nil || len(messages) != 0 || model.requestCount() != 0 {
					t.Fatalf("canceled preparation continued: messages=%d modelCalls=%d err=%v", len(messages), model.requestCount(), err)
				}
			})
		}
	}
}

func TestExecutionPreparationCancellationCleansUp(t *testing.T) {
	for _, reason := range []string{"cancel", "timeout"} {
		t.Run(reason, func(t *testing.T) {
			f := newExecutionFixture(t, &fakeLLM{}, newTestStore(t))
			wantErr := context.Canceled
			if reason == "timeout" {
				f.execution.responseTimeout = 20 * time.Millisecond
				wantErr = context.DeadlineExceeded
			}
			f.chat.PromptBuilder.System = dialogue.NewSystemPromptManager(preparationPromptSource(func(pctx context.Context, _ dialogue.SystemPromptRequest) ([]dialogue.SystemPromptPart, error) {
				if reason == "cancel" {
					f.opts.Requests.Cancel(contextinfo.RootRequestIDFromContext(pctx))
				} else {
					select {
					case <-pctx.Done():
					case <-time.After(time.Second):
						t.Fatal("preparation did not time out")
					}
				}
				return []dialogue.SystemPromptPart{{Content: "test"}}, nil
			}))
			ctx, row, err := f.execution.resolveInput(context.Background(), "hello")
			if err != nil {
				t.Fatal(err)
			}
			execution := turn.NewExecution(storage.NewID())
			if err := f.execution.Run(turn.WithExecution(ctx, execution), row, "hello", f.out); err != nil {
				t.Fatalf("request cancellation returned a second user error: %v", err)
			}
			select {
			case <-execution.Done():
			default:
				t.Fatal("canceled preparation did not finish its execution")
			}
			if result := execution.Wait(context.Background()); !errors.Is(result.Err, wantErr) {
				t.Fatalf("execution result=%+v", result)
			}
			if len(f.opts.Requests.ListBySession(row.ID)) != 0 || f.opts.Turns.Snapshot(row.ID).Phase != turn.PhaseIdle {
				t.Fatal("preparation left an active request or Turn")
			}
		})
	}
}

func TestRiskConfirmationAcceptsResponseDuringPrompt(t *testing.T) {
	for _, command := range []string{"/confirm", "/reject no", "/stop"} {
		t.Run(command, func(t *testing.T) {
			f := newExecutionFixture(t, &fakeLLM{}, newTestStore(t))
			_, reqCtx, in, _ := f.begin(t)
			if !f.opts.Turns.StartToolPhase(in.Session.ID, "attempt") {
				t.Fatal("start tool phase")
			}
			var promptSeen, pending bool
			if err := f.hooks.Register(hook.Registration{Point: hook.PointPlatformMessageSent, Name: "confirmation.fast", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, error) {
				if strings.Contains(llm.SegmentsTextOnly(event.Message.Segments), "高风险工具调用等待确认") {
					promptSeen = true
					_, pending = f.opts.Turns.PendingRiskConfirmation(in.Session.ID)
					if !pending {
						f.opts.Requests.Cancel(in.RequestID)
					} else if err := f.confirmations.SubmitResponse(reqCtx, in.Session.ID, command); err != nil {
						t.Error(err)
					}
				}
				return event, nil
			})}); err != nil {
				t.Fatal(err)
			}
			result, err := f.confirmations.AwaitToolConfirmation(reqCtx, in.Session.ID, llm.ToolCallRequest{ID: "call", Name: "danger", Arguments: "{}"}, tool.RiskAssessment{Level: tool.RiskHigh}, "detail")
			if !promptSeen || !pending || err != nil || result.Allowed != (command == "/confirm") || result.Stopped != (command == "/stop") {
				t.Fatalf("promptSeen=%v pending=%v result=%+v err=%v", promptSeen, pending, result, err)
			}
			if _, pending := f.opts.Turns.PendingRiskConfirmation(in.Session.ID); pending {
				t.Fatal("resolved confirmation remains pending")
			}
		})
	}
}

func TestRiskConfirmationPromptFailureClearsWait(t *testing.T) {
	f := newExecutionFixture(t, &fakeLLM{}, newTestStore(t))
	_, reqCtx, in, _ := f.begin(t)
	reqCtx, cancel := context.WithTimeout(reqCtx, 30*time.Millisecond)
	defer cancel()
	f.opts.Turns.StartToolPhase(in.Session.ID, "attempt")
	failure := errors.New("prompt send failed")
	var pending bool
	if err := f.hooks.Register(hook.Registration{Point: hook.PointAgentOutputPrepared, Name: "confirmation.fail", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, error) {
		_, pending = f.opts.Turns.PendingRiskConfirmation(in.Session.ID)
		return event, failure
	})}); err != nil {
		t.Fatal(err)
	}
	_, err := f.confirmations.AwaitToolConfirmation(reqCtx, in.Session.ID, llm.ToolCallRequest{ID: "call", Name: "danger", Arguments: "{}"}, tool.RiskAssessment{Level: tool.RiskHigh}, "detail")
	if !pending || !errors.Is(err, failure) || f.opts.Turns.Snapshot(in.Session.ID).Phase != turn.PhaseIdle {
		t.Fatalf("pendingAtSend=%v phase=%s err=%v", pending, f.opts.Turns.Snapshot(in.Session.ID).Phase, err)
	}
}

func TestRiskConfirmationInvalidAttemptDoesNotSendPrompt(t *testing.T) {
	f := newExecutionFixture(t, &fakeLLM{}, newTestStore(t))
	_, reqCtx, in, _ := f.begin(t)
	f.opts.Turns.StopSession(in.Session.ID, "attempt")
	if !f.opts.Turns.StartExecution(in.Session.ID, turn.Input{Text: "new"}, turn.NewExecution("new"), "replacement") || !f.opts.Turns.StartToolPhase(in.Session.ID, "replacement") {
		t.Fatal("start replacement attempt")
	}
	promptSeen := false
	f.on(t, hook.PointAgentOutputPrepared, func(event hook.Event) hook.Event {
		promptSeen = true
		return event
	})
	result, err := f.confirmations.AwaitToolConfirmation(reqCtx, in.Session.ID, llm.ToolCallRequest{ID: "old", Name: "danger", Arguments: "{}"}, tool.RiskAssessment{Level: tool.RiskHigh}, "detail")
	if promptSeen || err != nil || !result.Stopped || !f.opts.Turns.MatchesAttempt(in.Session.ID, "replacement") || f.opts.Turns.Snapshot(in.Session.ID).Phase != turn.PhaseTool {
		t.Fatalf("promptSeen=%v result=%+v err=%v phase=%s", promptSeen, result, err, f.opts.Turns.Snapshot(in.Session.ID).Phase)
	}
}
