package input

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"elbot/internal/command"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func testCommandText(name string, args ...string) string { return command.Format("/", name, args...) }

type commandExecutorProbe struct {
	command.ControlCapabilities
	row        *storage.Session
	current    operationresult.Result[control.CurrentConfirmationResult]
	currentErr error
	respond    operationresult.Result[control.RespondConfirmationResult]
	respondErr error
	request    command.Request
	result     *command.Result
	failure    error
	sendErr    error
	continued  control.Continuation
	order      []string
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
	p.request, p.result, p.failure = req, &result, failure
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
	return ctx, operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func TestCommandContinuationRequiresSuccessfulDelivery(t *testing.T) {
	for _, failSend := range []bool{false, true} {
		probe := &commandExecutorProbe{}
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
		row:     &storage.Session{ID: "s"},
		current: operationresult.Result[control.CurrentConfirmationResult]{Status: operationresult.Success, Data: control.CurrentConfirmationResult{Kind: "append"}},
		respond: operationresult.Result[control.RespondConfirmationResult]{Status: operationresult.Success, Data: control.RespondConfirmationResult{Accepted: true, Continuation: &control.Continuation{Kind: "append", SessionID: "s"}}},
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

func TestConfirmationWithoutPendingItemReturnsShortNotice(t *testing.T) {
	probe := &commandExecutorProbe{row: &storage.Session{ID: "s"}}
	router := command.NewRouter([]string{"/"})
	if err := command.RegisterBuiltins(router, command.BuiltinDeps{Control: probe}); err != nil {
		t.Fatal(err)
	}
	executor := NewCommandExecutor(CommandExecutorDeps{Router: router, Sessions: probe, Identity: probe, Output: probe, Confirmations: probe, Input: probe})
	_, handled, err := executor.Handle(t.Context(), "/confirm")
	if !handled || err != nil || probe.failure != nil || !reflect.DeepEqual(probe.order, []string{"send"}) || probe.result == nil || probe.result.Data != (command.TextData{Content: "无待确认"}) {
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
			if probe.result == nil || probe.result.Status != tc.wantStatus {
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
