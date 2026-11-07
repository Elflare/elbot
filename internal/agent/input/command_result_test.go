package input

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/turn"
	"errors"
	"reflect"
	"testing"
)

type commandResultProbe struct {
	nextCtx  context.Context
	replyCtx context.Context
	riskName string
	CommandExecution
	CommandConfirmations
	row     *storage.Session
	phase   turn.Phase
	sendErr error
	result  *command.Result
	failure *command.Error
	order   []string
}

func (p *commandResultProbe) CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error) {
	if p.row == nil {
		return nil, nil, session.ErrNoCurrentSession
	}
	return p.row, nil, nil
}
func (p *commandResultProbe) Scope(context.Context) session.Scope { return session.Scope{} }
func (p *commandResultProbe) Snapshot(string) turn.Snapshot       { return turn.Snapshot{Phase: p.phase} }
func (p *commandResultProbe) CompactActive(string) bool           { return false }
func (p *commandResultProbe) SendCommandResult(ctx context.Context, _ string, r *command.Result, e *command.Error) (delivery.Receipt, error) {
	p.replyCtx = ctx
	p.order = append(p.order, "send")
	p.result, p.failure = r, e
	return delivery.Receipt{}, p.sendErr
}
func (p *commandResultProbe) ContinueCommandInput(ctx context.Context, _ command.Continuation) (context.Context, error) {
	p.order = append(p.order, "continue")
	return ctx, nil
}

func TestCommandContinuationRequiresSuccessfulDelivery(t *testing.T) {
	for _, failSend := range []bool{false, true} {
		p := &commandResultProbe{}
		if failSend {
			p.sendErr = errors.New("transport down")
		}
		router := command.NewRouter([]string{"/"})
		if err := router.Register(command.NewFunc(command.Info{Name: "go", MinRole: contextinfo.RoleUser}, func(context.Context, command.Request) (*command.Result, error) {
			p.order = append(p.order, "execute")
			return &command.Result{Kind: command.ResultKindText, Text: "accepted", Continuation: &command.Continuation{SessionID: "s", Text: "next"}}, nil
		})); err != nil {
			t.Fatal(err)
		}
		executor := NewCommandExecutor(CommandExecutorDeps{CommandText: testCommandText, Router: router, Sessions: p, Turns: p, Identity: p, Execution: p, Output: p, Confirmations: p, Input: p})
		_, handled, err := executor.Handle(t.Context(), "/go")
		want := []string{"execute", "send"}
		if !failSend {
			want = append(want, "continue")
		}
		if !handled || !reflect.DeepEqual(p.order, want) || !errors.Is(err, p.sendErr) {
			t.Fatalf("order=%v handled=%v err=%v", p.order, handled, err)
		}
	}
}

func TestCommandEntryErrorsUseStructuredReply(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		info        command.Info
		row         *storage.Session
		phase       turn.Phase
		code        string
	}{
		{name: "unknown", input: "/missing", info: command.Info{Name: "known", MinRole: contextinfo.RoleUser}, code: command.CodeUnknownCommand},
		{name: "permission", input: "/known", info: command.Info{Name: "known", MinRole: contextinfo.RoleSuperadmin}, code: command.CodePermissionDenied},
		{name: "busy", input: "/known", info: command.Info{Name: "known", MinRole: contextinfo.RoleUser, SessionEffect: command.SessionEffectSwitchCurrent}, row: &storage.Session{ID: "s"}, phase: turn.PhaseLLM, code: command.CodeSessionBusy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &commandResultProbe{row: tc.row, phase: tc.phase}
			router := command.NewRouter([]string{"/"})
			if err := router.Register(command.NewFunc(tc.info, func(context.Context, command.Request) (*command.Result, error) {
				t.Fatal("handler executed")
				return nil, nil
			})); err != nil {
				t.Fatal(err)
			}
			executor := NewCommandExecutor(CommandExecutorDeps{CommandText: testCommandText, Router: router, Sessions: p, Turns: p, Identity: p, Execution: p, Output: p, Confirmations: p, Input: p})
			ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleUser})
			_, handled, err := executor.Handle(ctx, tc.input)
			if !handled || p.failure == nil || p.failure.Code != tc.code || !errors.Is(err, p.failure) || !reflect.DeepEqual(p.order, []string{"send"}) {
				t.Fatalf("failure=%+v order=%v err=%v", p.failure, p.order, err)
			}
		})
	}
}

func (p *commandResultProbe) CommandInfo(string) (command.Info, bool) {
	return command.Info{Name: p.riskName}, p.riskName != ""
}
func (p *commandResultProbe) ResumeAppend(ctx context.Context, _ *storage.Session, _ string) (context.Context, error) {
	p.order = append(p.order, "append")
	if p.nextCtx != nil {
		return p.nextCtx, nil
	}
	return ctx, nil
}

func TestAppendControlOnlyInterceptsPendingConfirmation(t *testing.T) {
	for _, pending := range []bool{false, true} {
		p := &commandResultProbe{row: &storage.Session{ID: "s"}, phase: turn.PhaseIdle}
		if pending {
			p.phase = turn.PhaseAwaitAppendConfirm
		}
		executor := NewCommandExecutor(CommandExecutorDeps{Router: command.NewRouter([]string{"/"}), Sessions: p, Turns: p, Identity: p, Execution: p, Output: p, Confirmations: p})
		_, handled, err := executor.Handle(t.Context(), "y")
		if err != nil || handled != pending {
			t.Fatalf("handled=%v pending=%v err=%v", handled, pending, err)
		}
		if pending && !reflect.DeepEqual(p.order, []string{"append", "send"}) {
			t.Fatalf("order=%v", p.order)
		}
		if !pending && len(p.order) != 0 {
			t.Fatal(p.order)
		}
	}
}

type commandOriginKey struct{}

func TestCommandReplyRetainsOriginalInputContext(t *testing.T) {
	p := &commandResultProbe{row: &storage.Session{ID: "s"}, phase: turn.PhaseAwaitAppendConfirm, nextCtx: context.WithValue(t.Context(), commandOriginKey{}, "execution")}
	executor := NewCommandExecutor(CommandExecutorDeps{Router: command.NewRouter([]string{"/"}), Sessions: p, Turns: p, Identity: p, Execution: p, Output: p, Confirmations: p})
	ctx := context.WithValue(t.Context(), commandOriginKey{}, "input")
	if _, handled, err := executor.Handle(ctx, "y"); err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if p.replyCtx.Value(commandOriginKey{}) != "input" {
		t.Fatal("reply followed execution context")
	}
}
func TestExpiredAppendButtonIsRejected(t *testing.T) {
	p := &commandResultProbe{riskName: "confirm"}
	executor := NewCommandExecutor(CommandExecutorDeps{Router: command.NewRouter([]string{"/"}), Sessions: p, Turns: p, Identity: p, Execution: p, Output: p, Confirmations: p})
	_, handled, err := executor.Handle(t.Context(), "/confirm")
	if !handled || err == nil || p.failure.Code != command.CodeConfirmationExpired || !reflect.DeepEqual(p.order, []string{"send"}) {
		t.Fatalf("handled=%v failure=%+v order=%v err=%v", handled, p.failure, p.order, err)
	}
}

func TestExecutionFailureAndDeliveryFailureStaySeparate(t *testing.T) {
	executionErr, sendErr := errors.New("storage offline"), errors.New("connection offline")
	p := &commandResultProbe{sendErr: sendErr}
	var logs []events.LogRecord
	connection, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) {
		if r.Module == "command" {
			logs = append(logs, r)
		}
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	router := command.NewRouter([]string{"/"})
	if err := router.Register(command.NewFunc(command.Info{Name: "partial", MinRole: contextinfo.RoleUser}, func(context.Context, command.Request) (*command.Result, error) {
		return &command.Result{Kind: command.ResultKindText, Text: "one item completed", Continuation: &command.Continuation{Text: "never run"}}, executionErr
	})); err != nil {
		t.Fatal(err)
	}
	executor := NewCommandExecutor(CommandExecutorDeps{Router: router, Sessions: p, Turns: p, Identity: p, Output: p, Confirmations: p, Input: p})
	_, _, err = executor.Handle(t.Context(), "/partial")
	if !errors.Is(err, sendErr) || !errors.Is(p.failure, executionErr) || p.result.Text != "one item completed" || !reflect.DeepEqual(p.order, []string{"send"}) {
		t.Fatalf("result=%+v failure=%+v err=%v order=%v", p.result, p.failure, err, p.order)
	}
	if len(logs) != 2 || logs[0].EventName != "command_failed" || logs[1].EventName != "command_send_failed" {
		t.Fatalf("logs=%+v", logs)
	}
}
