package command

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/tool"
)

type builtinControlStub struct {
	ControlCapabilities
	commands []control.CommandInfo
	remove   *control.RemoveSkillRequest
}

func (s *builtinControlStub) ListCommands(ctx context.Context, req control.ListCommandsRequest) (control.ListCommandsResult, error) {
	actor, _ := contextinfo.ActorFromContext(ctx)
	result := control.ListCommandsResult{Items: []control.CommandInfo{}}
	for _, item := range s.commands {
		if req.Name != "" && item.Name != req.Name && !slices.Contains(item.Aliases, req.Name) {
			continue
		}
		if item.MinRole != contextinfo.RoleUser && actor.Role != contextinfo.RoleSuperadmin {
			continue
		}
		result.Items = append(result.Items, item)
	}
	if req.Name != "" && len(result.Items) == 0 {
		return result, &control.Error{Code: "not_found", Message: "command unavailable"}
	}
	return result, nil
}

func (*builtinControlStub) QueryTools(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error) {
	return nil, nil
}

func (s *builtinControlStub) RemoveSkill(_ context.Context, req control.RemoveSkillRequest) error {
	s.remove = &req
	return nil
}

func TestCommandsUseConfiguredPrefixAndReturnConfirmationAction(t *testing.T) {
	router := NewRouter([]string{"!", "!!"})
	controlStub := &builtinControlStub{}
	if err := RegisterBuiltins(router, BuiltinDeps{Control: controlStub, SessionListPageSize: 10, SessionRetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	result, err := router.Dispatch(ctx, "!!tools remove echo")
	if err != nil || result == nil || result.Confirmation == nil || len(result.Confirmation.Actions) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := result.Confirmation.Actions[0].Command; got != "!tools remove echo --confirm" {
		t.Fatalf("confirmation command=%q", got)
	}
	if controlStub.remove != nil {
		t.Fatal("unconfirmed request reached skill removal")
	}

	if _, err := router.Dispatch(ctx, "!!doctor invalid"); err == nil || !strings.Contains(err.Error(), "usage: !doctor") {
		t.Fatalf("doctor usage error=%v", err)
	}
	result, err = router.Dispatch(ctx, "!!tools remove echo --confirm")
	if err != nil || controlStub.remove == nil || controlStub.remove.Name != "echo" || !controlStub.remove.Confirmed {
		t.Fatalf("remove=%+v err=%v", controlStub.remove, err)
	}
}

func TestHelpPassesLookupToControlAndPreservesReturnedData(t *testing.T) {
	controlStub := &builtinControlStub{commands: []control.CommandInfo{
		{Name: "public", Description: "Public command.", MinRole: contextinfo.RoleUser},
		{Name: "secret", Description: "Private command."},
	}}
	router := NewRouter([]string{"/"})
	if err := RegisterBuiltins(router, BuiltinDeps{Control: controlStub}); err != nil {
		t.Fatal(err)
	}
	help, _ := router.Handler("help")
	userCtx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleUser})
	list, err := help.Handle(userCtx, Request{Prefix: "/"})
	if err != nil {
		t.Fatal(err)
	}
	data, ok := list.Data.(control.ListCommandsResult)
	if !ok || len(data.Items) != 1 || data.Items[0].Name != "public" {
		t.Fatalf("help result=%#v", list.Data)
	}
	if _, err := help.Handle(userCtx, Request{Prefix: "/", Args: "secret"}); err == nil {
		t.Fatal("control lookup error was not returned")
	}
	completer := help.(Completer)
	if got := completer.Complete(userCtx, CompletionRequest{Raw: "/help p", Prefix: "/", Name: "help", Args: "p", Cursor: len("/help p")}); len(got) != 1 || got[0].Text != "public" {
		t.Fatalf("completions=%#v", got)
	}
	adminCtx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	if got := completer.Complete(adminCtx, CompletionRequest{Raw: "/help s", Prefix: "/", Name: "help", Args: "s", Cursor: len("/help s")}); len(got) != 1 || got[0].Text != "secret" {
		t.Fatalf("admin completions=%#v", got)
	}
}

func TestDoctorRegistrationPermissionsAndHelp(t *testing.T) {
	router := NewRouter([]string{"/"})
	if err := RegisterBuiltins(router, BuiltinDeps{Control: &builtinControlStub{commands: []control.CommandInfo{{Name: "doctor", MinRole: contextinfo.RoleSuperadmin}}}}); err != nil {
		t.Fatal(err)
	}
	admin := contextinfo.Actor{Role: contextinfo.RoleSuperadmin}
	user := contextinfo.Actor{Role: contextinfo.RoleUser}
	info, ok := router.CommandInfo("doctor")
	if !ok || CanAccess(info, user) || !CanAccess(info, admin) || info.SessionEffect != SessionEffectNone {
		t.Fatalf("doctor info = %#v", info)
	}
	if !slices.Contains(router.CompleteForActor("/doc", admin), "/doctor") || len(router.CompleteForActor("/doc", user)) != 0 {
		t.Fatal("doctor completion permissions are incorrect")
	}
	for _, actor := range []contextinfo.Actor{admin, user} {
		ctx := contextinfo.WithActor(t.Context(), actor)
		result, err := router.Dispatch(ctx, "/help doctor")
		if actor.Role != contextinfo.RoleSuperadmin {
			var failure *Error
			var data control.ListCommandsResult
			ok := false
			if result != nil {
				data, ok = result.Data.(control.ListCommandsResult)
			}
			if !ok || len(data.Items) != 0 || !errors.As(err, &failure) || failure.Code != CodeNotFound {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}
