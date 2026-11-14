package input

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"elbot/internal/command"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func testCommandText(name string, args ...string) string { return command.Format("/", name, args...) }

type commandExecutorProbe struct {
	command.ControlCapabilities
	row            *storage.Session
	current        operationresult.Result[control.CurrentConfirmationResult]
	currentErr     error
	respond        operationresult.Result[control.RespondConfirmationResult]
	respondErr     error
	request        command.Request
	result         command.Result
	sentResult     []command.Result
	failure        error
	sendErr        error
	continued      control.Continuation
	continueResult operationresult.Result[struct{}]
	order          []string
}

func (p *commandExecutorProbe) CurrentBound(context.Context, session.Scope) (operationresult.Result[session.CurrentBoundData], error) {
	if p.row == nil {
		return operationresult.Result[session.CurrentBoundData]{Status: operationresult.NoCurrentSession, Message: "当前没有会话"}, nil
	}
	return operationresult.Result[session.CurrentBoundData]{Status: operationresult.Success, Data: session.CurrentBoundData{Session: p.row}}, nil
}
func (*commandExecutorProbe) Scope(context.Context) session.Scope { return session.Scope{} }
func (p *commandExecutorProbe) SendCommandResult(_ context.Context, req command.Request, result command.Result, failure error) (delivery.Receipt, error) {
	p.order = append(p.order, "send")
	p.request, p.result, p.failure = req, result, failure
	p.sentResult = append(p.sentResult, result)
	return delivery.Receipt{}, p.sendErr
}
func (p *commandExecutorProbe) CurrentConfirmation(context.Context, control.CurrentConfirmationRequest) (operationresult.Result[control.CurrentConfirmationResult], error) {
	return p.current, p.currentErr
}
func (p *commandExecutorProbe) RespondConfirmation(context.Context, control.RespondConfirmationRequest) (operationresult.Result[control.RespondConfirmationResult], error) {
	return p.respond, p.respondErr
}
func (p *commandExecutorProbe) ContinueCommandInput(ctx context.Context, continuation control.Continuation) (context.Context, operationresult.Result[struct{}], error) {
	p.order = append(p.order, "continue")
	p.continued = continuation
	return ctx, p.continueResult, nil
}

func TestCommandContinuationRequiresSuccessfulDelivery(t *testing.T) {
	for _, failSend := range []bool{false, true} {
		probe := &commandExecutorProbe{continueResult: operationresult.Result[struct{}]{Status: operationresult.Success}}
		if failSend {
			probe.sendErr = errors.New("transport down")
		}
		router := command.NewRouter([]string{"/"})
		continuation := control.Continuation{Kind: "input", SessionID: "s", Text: "next"}
		if err := router.Register(command.NewFunc(command.Info{Name: "go", MinRole: "user"}, func(context.Context, command.Request) (command.Result, error) {
			probe.order = append(probe.order, "execute")
			return command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Data: command.TextData{Content: "accepted"}, Continuation: &continuation}, nil
		})); err != nil {
			t.Fatal(err)
		}
		executor := NewCommandExecutor(CommandExecutorDeps{Router: router, Sessions: probe, Identity: probe, Output: probe, Confirmations: probe, Input: probe})
		_, handled, err := executor.Handle(t.Context(), "/go")
		want := []string{"execute", "send"}
		if !failSend {
			want = append(want, "continue")
		}
		if !handled || !reflect.DeepEqual(probe.order, want) || !errors.Is(err, probe.sendErr) {
			t.Fatalf("order=%v handled=%v err=%v", probe.order, handled, err)
		}
		if !failSend && probe.continued != continuation {
			t.Fatalf("continuation=%+v", probe.continued)
		}
	}
}

func TestAppendConfirmationUsesControlAndContinuesAfterDelivery(t *testing.T) {
	probe := &commandExecutorProbe{
		row:            &storage.Session{ID: "s"},
		continueResult: operationresult.Result[struct{}]{Status: operationresult.Success},
		current:        operationresult.Result[control.CurrentConfirmationResult]{Status: operationresult.Success, Data: control.CurrentConfirmationResult{Kind: "append"}},
		respond:        operationresult.Result[control.RespondConfirmationResult]{Status: operationresult.Success, Data: control.RespondConfirmationResult{Accepted: true, Continuation: &control.Continuation{Kind: "append", SessionID: "s"}}},
	}
	router := command.NewRouter([]string{"/"})
	if err := command.RegisterBuiltins(router, command.BuiltinDeps{Control: probe}); err != nil {
		t.Fatal(err)
	}
	executor := NewCommandExecutor(CommandExecutorDeps{Router: router, Sessions: probe, Identity: probe, Output: probe, Confirmations: probe, Input: probe})
	_, handled, err := executor.Handle(t.Context(), "/confirm")
	if err != nil || !handled || !reflect.DeepEqual(probe.order, []string{"send", "continue"}) || probe.result.Continuation == nil || probe.result.Continuation.Kind != "append" || probe.result.Continuation.SessionID != "s" {
		t.Fatalf("result=%+v order=%v handled=%v err=%v", probe.result, probe.order, handled, err)
	}
}

func TestBusinessContinuationFailureIsSentAsResult(t *testing.T) {
	probe := &commandExecutorProbe{continueResult: operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: "session disappeared"}}
	router := command.NewRouter([]string{"/"})
	continuation := control.Continuation{Kind: "input", SessionID: "missing", Text: "next"}
	if err := router.Register(command.NewFunc(command.Info{Name: "go", MinRole: "user"}, func(context.Context, command.Request) (command.Result, error) {
		return command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Data: command.TextData{Content: "accepted"}, Continuation: &continuation}, nil
	})); err != nil {
		t.Fatal(err)
	}
	executor := NewCommandExecutor(CommandExecutorDeps{Router: router, Sessions: probe, Identity: probe, Output: probe, Confirmations: probe, Input: probe})
	_, handled, err := executor.Handle(t.Context(), "/go")
	if !handled || err != nil || !reflect.DeepEqual(probe.order, []string{"execute", "send", "continue", "send"}) || len(probe.sentResult) != 2 {
		t.Fatalf("handled=%v results=%+v order=%v err=%v", handled, probe.sentResult, probe.order, err)
	}
	if got := probe.sentResult[1]; got.Status != operationresult.NotFound || got.Message != "session disappeared" || got.Data != nil {
		t.Fatalf("continuation result=%+v", got)
	}
}

func TestConfirmationWithoutPendingItemReturnsShortNotice(t *testing.T) {
	probe := &commandExecutorProbe{
		row:     &storage.Session{ID: "s"},
		current: operationresult.Result[control.CurrentConfirmationResult]{Status: operationresult.Success},
	}
	router := command.NewRouter([]string{"/"})
	if err := command.RegisterBuiltins(router, command.BuiltinDeps{Control: probe}); err != nil {
		t.Fatal(err)
	}
	executor := NewCommandExecutor(CommandExecutorDeps{Router: router, Sessions: probe, Identity: probe, Output: probe, Confirmations: probe, Input: probe})
	_, handled, err := executor.Handle(t.Context(), "/confirm")
	if !handled || err != nil || probe.failure != nil || !reflect.DeepEqual(probe.order, []string{"send"}) || probe.result.Data != (command.TextData{Content: "无待确认"}) {
		t.Fatalf("handled=%v result=%+v failure=%+v order=%v err=%v", handled, probe.result, probe.failure, probe.order, err)
	}
}

func TestCommandErrorsAndBusinessRejectionsStaySeparate(t *testing.T) {
	tests := []struct {
		name       string
		result     command.Result
		cause      error
		wantStatus operationresult.Status
	}{
		{
			name:       "business rejection",
			result:     command.Result{Status: operationresult.Forbidden, Message: "无权执行该操作"},
			wantStatus: operationresult.Forbidden,
		},
		{
			name:       "program failure",
			result:     command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Data: command.TextData{Content: "partial result"}},
			cause:      errors.New("underlying failure"),
			wantStatus: operationresult.Success,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			probe := &commandExecutorProbe{}
			router := command.NewRouter([]string{"/"})
			if err := router.Register(command.NewFunc(command.Info{Name: "restricted", MinRole: "user"}, func(context.Context, command.Request) (command.Result, error) {
				return tc.result, tc.cause
			})); err != nil {
				t.Fatal(err)
			}

			executor := NewCommandExecutor(CommandExecutorDeps{Router: router, Sessions: probe, Identity: probe, Output: probe})
			_, handled, err := executor.Handle(t.Context(), "/restricted")
			if !handled {
				t.Fatal("command was not handled")
			}
			if tc.cause != nil {
				if err == nil || probe.failure == nil || !errors.Is(err, tc.cause) || !errors.Is(probe.failure, tc.cause) {
					t.Fatalf("program failure was not preserved: returned=%v sent=%v", err, probe.failure)
				}
			} else if err != nil || probe.failure != nil {
				t.Fatalf("business rejection became Go error: returned=%v sent=%v", err, probe.failure)
			}
			if probe.result.Status != tc.wantStatus {
				t.Fatalf("result=%+v, want status %q", probe.result, tc.wantStatus)
			}
		})
	}
}

func TestCommandRequestIsPassedToOutput(t *testing.T) {
	probe := &commandExecutorProbe{}
	router := command.NewRouter([]string{"!"})
	if err := router.Register(command.NewFunc(command.Info{Name: "echo", Aliases: []string{"e"}, MinRole: "user"}, func(context.Context, command.Request) (command.Result, error) {
		return command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Data: command.TextData{Content: "ok"}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	executor := NewCommandExecutor(CommandExecutorDeps{Router: router, Sessions: probe, Identity: probe, Output: probe})
	_, handled, err := executor.Handle(t.Context(), "!e keep this")
	if err != nil || !handled || probe.request.Name != "echo" || probe.request.Args != "keep this" || probe.request.Raw != "!e keep this" || probe.request.Prefix != "!" {
		t.Fatalf("request=%+v handled=%v err=%v", probe.request, handled, err)
	}
}

type normalizationProbe struct {
	InputExecution
	InputOutput
	released bool
	messages []string
}

func (p *normalizationProbe) EnterInput(ctx context.Context, _ *storage.Session) (context.Context, operationresult.Result[struct{}], func(), error) {
	return ctx, operationresult.Result[struct{}]{Status: operationresult.Success}, func() { p.released = true }, nil
}

func (p *normalizationProbe) Send(_ context.Context, event delivery.StreamEvent) (delivery.Receipt, error) {
	p.messages = append(p.messages, event.Text)
	return delivery.Receipt{}, nil
}

func TestInputNormalizationStopsAtBusinessRejectionOrFault(t *testing.T) {
	failure := errors.New("read workspace: disk failure")
	for _, fault := range []bool{false, true} {
		probe := &normalizationProbe{}
		coordinator := NewInputCoordinator(InputCoordinatorDeps{Execution: probe, Output: probe,
			NormalizeInput: func(context.Context, string, []llm.MessageSegment) (operationresult.Result[[]llm.MessageSegment], error) {
				if !probe.released {
					t.Fatal("normalization holds input admission")
				}
				if fault {
					return operationresult.Result[[]llm.MessageSegment]{}, failure
				}
				return operationresult.Result[[]llm.MessageSegment]{Status: operationresult.InvalidArguments, Message: "invalid workspace reference"}, nil
			},
		})
		_, err := coordinator.handleSessionInput(t.Context(), &storage.Session{ID: "session"}, "input")
		if fault {
			if !errors.Is(err, failure) || len(probe.messages) != 0 {
				t.Fatalf("fault: err=%v messages=%v", err, probe.messages)
			}
		} else if err != nil || !reflect.DeepEqual(probe.messages, []string{"invalid workspace reference"}) {
			t.Fatalf("rejection: err=%v messages=%v", err, probe.messages)
		}
	}
}
