package builtin

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"

	command "elbot/internal/command/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type coagentControlStub struct {
	command.ControlCapabilities
	actions []session.CoagentAction
	status  []string
	board   storage.CoagentBoard
}

func (s *coagentControlStub) Coagent(_ context.Context, a session.CoagentAction, user bool) (operationresult.Result[storage.CoagentBoard], error) {
	if !user {
		panic("expected user command")
	}
	s.actions = append(s.actions, a)
	return operationresult.Result[storage.CoagentBoard]{Status: operationresult.Success, Data: s.board}, nil
}
func (s *coagentControlStub) CoagentStatus(_ context.Context, id string) (operationresult.Result[[]control.CoagentStatus], error) {
	s.status = append(s.status, id)
	return operationresult.Result[[]control.CoagentStatus]{Status: operationresult.Success}, nil
}

func TestCoagentCompletionUsesOnlyBoardRoster(t *testing.T) {
	stub := &coagentControlStub{board: storage.CoagentBoard{MainSessionID: "main", Members: []storage.CoagentMember{{SessionID: "main", Name: "主"}, {SessionID: "child", Name: "同伴"}}}}
	c := coagentCommand{Deps{Control: stub}}
	for _, tc := range []struct {
		args string
		want []string
	}{
		{"", []string{"create", "post", "pin", "board", "status", "remove"}}, {"s", []string{"status"}}, {"pin --g", []string{"--goal"}}, {"post -", []string{"--to"}},
		{"post --to c", []string{"child"}}, {"status ", []string{"main", "child"}}, {"remove ", []string{"child"}}, {"create ", []string{"1", "2", "3", "4", "5", "6", "7"}}, {"post hello ", nil}, {"pin --goal ", nil},
	} {
		raw := "/co " + tc.args
		got := c.Complete(t.Context(), command.CompletionRequest{Raw: raw, Cursor: len(raw), Name: "co", Prefix: "/", Args: tc.args})
		var text []string
		for _, opt := range got {
			text = append(text, opt.Text)
			if opt.ReplaceEnd != len(raw) || !strings.HasPrefix(opt.Text, raw[opt.ReplaceStart:opt.ReplaceEnd]) {
				t.Fatal(opt)
			}
		}
		if !slices.Equal(text, tc.want) {
			t.Errorf("%q: got %v, want %v", raw, text, tc.want)
		}
	}
	for _, a := range stub.actions {
		if a.Operation != "board" || a.After != math.MaxInt64 {
			t.Fatal("completion accessed history or wrote to board", a)
		}
	}
}

type coagentViewStub struct{ main, member string }

func (v *coagentViewStub) SelectCoagentView(main, member string) { v.main, v.member = main, member }

func TestResumeCoagentChangesWindowWithoutRebinding(t *testing.T) {
	stub := &coagentControlStub{board: storage.CoagentBoard{MainSessionID: "main", Members: []storage.CoagentMember{{SessionID: "main"}, {SessionID: "child", Name: "同伴"}}}}
	c := NewResume(Deps{Control: stub, CommandText: testCommandText})
	v := &coagentViewStub{}
	resume := func(target string) operationresult.Status {
		ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{PlatformData: v, CoagentMainSessionID: v.main, TargetSessionID: v.member})
		r, err := c.Handle(ctx, command.Request{Name: "resume", Args: target})
		if err != nil {
			t.Fatal(err)
		}
		return r.Status
	}
	if resume("child") != operationresult.Success || v.main != "main" || v.member != "child" {
		t.Fatal(v)
	}
	if resume("outside") != operationresult.Rejected || v.member != "child" {
		t.Fatal(v)
	}
	stub.board.Members = stub.board.Members[:1] // 移除后仍可退出视图。
	if resume("main") != operationresult.Success || *v != (coagentViewStub{}) {
		t.Fatal(v)
	}
}
func TestCoagentStatusAndRemoveCommands(t *testing.T) {
	stub := &coagentControlStub{}
	c := coagentCommand{Deps{Control: stub, CommandText: testCommandText}}
	for _, args := range []string{"status", "status child", "remove child"} {
		res, err := c.Handle(t.Context(), command.Request{Args: args})
		if err != nil || res.Status != operationresult.Success {
			t.Fatal(args, res, err)
		}
	}
	for _, args := range []string{"remove", "remove child extra", "status child extra"} {
		res, err := c.Handle(t.Context(), command.Request{Args: args})
		if err != nil || res.Status != operationresult.InvalidArguments {
			t.Fatal(args, res, err)
		}
	}
	if !slices.Equal(stub.status, []string{"", "child"}) || len(stub.actions) != 1 || stub.actions[0].Operation != "remove" || stub.actions[0].TargetSessionID != "child" {
		t.Fatal(stub)
	}
}
