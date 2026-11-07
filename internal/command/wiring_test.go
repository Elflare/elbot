package command

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/doctor"
	"elbot/internal/tool"
)

type builtinToolsStub struct{}

func (*builtinToolsStub) QueryTools(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error) {
	return nil, nil
}
func (*builtinToolsStub) Reload(context.Context) error         { return nil }
func (*builtinToolsStub) Remove(context.Context, string) error { return nil }

type doctorStub struct {
	report doctor.Report
	err    error
	calls  int
}

func (d *doctorStub) Check(context.Context) (doctor.Report, error) {
	d.calls++
	return d.report, d.err
}

func TestCommandsUseConfiguredPrefixInsteadOfRequestPrefix(t *testing.T) {
	router := NewRouter([]string{"!", "!!"})
	tools := &builtinToolsStub{}
	deps := BuiltinDeps{Tools: tools, Skills: tools, SessionListPageSize: 10, SessionRetentionDays: 30}
	if err := RegisterBuiltins(router, deps); err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	for _, tc := range []struct{ input, want string }{
		{"!!tools remove echo", "!tools remove echo --confirm"},
		{"!!doctor invalid", "usage: !doctor"},
	} {
		result, err := router.Dispatch(ctx, tc.input)
		if err != nil {
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: %v", tc.input, err)
			}
			continue
		}
		if result == nil || !strings.Contains(result.Text, tc.want) || strings.Contains(result.Text, "!!") {
			t.Fatalf("%s: %+v", tc.input, result)
		}
	}
}

func TestHelpCommandFiltersByActor(t *testing.T) {
	router := NewRouter([]string{"/"})
	deps := BuiltinDeps{SessionListPageSize: 10, SessionRetentionDays: 30}
	if err := RegisterBuiltins(router, deps); err != nil {
		t.Fatalf("register help: %v", err)
	}
	if err := router.Register(NewFunc(Info{Name: "public", Args: "", Description: "Public command.", MinRole: contextinfo.RoleUser}, nil)); err != nil {
		t.Fatalf("register public: %v", err)
	}
	if err := router.Register(NewFunc(Info{Name: "secret", Args: "", Description: "Private command.", Aliases: []string{"sec"}}, nil)); err != nil {
		t.Fatalf("register secret: %v", err)
	}

	help, _ := router.Handler("help")
	userCtx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleUser})
	list, err := help.Handle(userCtx, Request{Prefix: "/"})
	if err != nil {
		t.Fatalf("user help: %v", err)
	}
	if !strings.Contains(list.Text, "/help") || !strings.Contains(list.Text, "/public") || strings.Contains(list.Text, "/secret") {
		t.Fatalf("user help content = %q", list.Text)
	}
	privateDetail, err := help.Handle(userCtx, Request{Prefix: "/", Args: "sec"})
	var failure *Error
	if privateDetail != nil || !errors.As(err, &failure) || failure.Code != CodeUnknownCommand || failure.Message != "unk command: sec" {
		t.Fatalf("result=%+v err=%v", privateDetail, err)
	}
	publicDetail, err := help.Handle(userCtx, Request{Prefix: "/", Args: "public"})
	if err != nil || !strings.Contains(publicDetail.Text, "/public") {
		t.Fatalf("public help = %#v, %v", publicDetail, err)
	}

	completer := help.(Completer)
	if got := completer.Complete(userCtx, CompletionRequest{Raw: "/help sec", Prefix: "/", Name: "help", Args: "sec", Cursor: len("/help sec")}); len(got) != 0 {
		t.Fatalf("user private completions = %#v", got)
	}

	adminCtx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	adminList, err := help.Handle(adminCtx, Request{Prefix: "/"})
	if err != nil || !strings.Contains(adminList.Text, "/secret") {
		t.Fatalf("admin help = %#v, %v", adminList, err)
	}
	adminDetail, err := help.Handle(adminCtx, Request{Prefix: "/", Args: "sec"})
	if err != nil || !strings.Contains(adminDetail.Text, "/secret") {
		t.Fatalf("admin private help = %#v, %v", adminDetail, err)
	}
	if got := completer.Complete(adminCtx, CompletionRequest{Raw: "/help sec", Prefix: "/", Name: "help", Args: "sec", Cursor: len("/help sec")}); len(got) != 1 || got[0].Text != "secret" {
		t.Fatalf("admin private completions = %#v", got)
	}
}

func TestDoctorRegistrationPermissionsHelpAndCompletion(t *testing.T) {
	router := NewRouter([]string{"/"})
	deps := BuiltinDeps{Doctor: &doctorStub{}, SessionListPageSize: 10, SessionRetentionDays: 30}
	if err := RegisterBuiltins(router, deps); err != nil {
		t.Fatal(err)
	}
	admin := contextinfo.Actor{Role: contextinfo.RoleSuperadmin}
	user := contextinfo.Actor{Role: contextinfo.RoleUser}
	info, ok := router.CommandInfo("doctor")
	if !ok || CanAccess(info, user) || !CanAccess(info, admin) || info.SessionEffect != SessionEffectNone {
		t.Fatalf("doctor info = %#v", info)
	}
	if !slices.Contains(router.CompleteForActor("/doc", admin), "/doctor") {
		t.Fatal("missing admin completion")
	}
	if got := router.CompleteForActor("/doc", user); len(got) != 0 {
		t.Fatalf("user completion = %#v", got)
	}
	for _, actor := range []contextinfo.Actor{admin, user} {
		ctx := contextinfo.WithActor(context.Background(), actor)
		result, err := router.Dispatch(ctx, "/help doctor")
		if actor.Role != contextinfo.RoleSuperadmin {
			var failure *Error
			if result != nil || !errors.As(err, &failure) || failure.Code != CodeUnknownCommand {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}
