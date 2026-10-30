package builtin

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
)

func TestHelpCommandShowsDetailedHelp(t *testing.T) {
	router := command.NewRouter([]string{"/"})
	deps := Deps{CommandText: testCommandText, Router: router}
	if err := RegisterFactories(router, deps, NewHelp); err != nil {
		t.Fatalf("register help: %v", err)
	}
	if err := router.Register(command.NewFunc(command.Info{
		Name:        "audit",
		Args:        "[options]",
		Description: "Show audit events.",
		Aliases:     []string{"aud"},
		Help:        "Options:\n  --event <name>",
	}, nil)); err != nil {
		t.Fatalf("register audit: %v", err)
	}

	ctx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	result, err := NewHelp(deps).Handle(ctx, command.Request{Prefix: "/", Args: "aud"})
	if err != nil {
		t.Fatalf("help handle: %v", err)
	}
	for _, want := range []string{"/audit [options]", "Show audit events.", "aliases: aud", "--event <name>"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("help content missing %q: %q", want, result.Content)
		}
	}
}

func TestCommandsUseConfiguredPrefixInsteadOfRequestPrefix(t *testing.T) {
	router := command.NewRouter([]string{"!", "!!"})
	commandText := func(name string, args ...string) string { return command.Format("!", name, args...) }
	tools := &fakeToolService{}
	deps := Deps{CommandText: commandText, Router: router, Tools: tools, Skills: tools}
	if err := RegisterDefaultModules(router, deps); err != nil {
		t.Fatal(err)
	}
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	for _, tc := range []struct{ input, want string }{
		{"!!help", "!stop [request_id|number]"},
		{"!!help model", "!model [--chat"},
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
		if result == nil || !strings.Contains(result.Content, tc.want) || strings.Contains(result.Content, "!!") {
			t.Fatalf("%s: %+v", tc.input, result)
		}
	}
	page := formatSessionsPage(nil, "", 2, "query text", true, commandText, "sessions")
	if !strings.Contains(page, "prev: !sessions 1 query text") || !strings.Contains(page, "next: !sessions 3 query text") {
		t.Fatal(page)
	}
	resume := formatResumableSessionsPage(nil, 2, 10, true, commandText)
	if !strings.Contains(resume, "prev: !resume --page 1") || !strings.Contains(resume, "next: !resume --page 3") {
		t.Fatal(resume)
	}
}

func TestHelpCommandFiltersByActor(t *testing.T) {
	router := command.NewRouter([]string{"/"})
	deps := Deps{CommandText: testCommandText, Router: router}
	if err := RegisterFactories(router, deps, NewHelp); err != nil {
		t.Fatalf("register help: %v", err)
	}
	if err := router.Register(command.NewFunc(command.Info{Name: "public", Args: "", Description: "Public command.", MinRole: contextinfo.RoleUser}, nil)); err != nil {
		t.Fatalf("register public: %v", err)
	}
	if err := router.Register(command.NewFunc(command.Info{Name: "secret", Args: "", Description: "Private command.", Aliases: []string{"sec"}}, nil)); err != nil {
		t.Fatalf("register secret: %v", err)
	}

	help := NewHelp(deps)
	userCtx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleUser})
	list, err := help.Handle(userCtx, command.Request{Prefix: "/"})
	if err != nil {
		t.Fatalf("user help: %v", err)
	}
	if !strings.Contains(list.Content, "/help") || !strings.Contains(list.Content, "/public") || strings.Contains(list.Content, "/secret") {
		t.Fatalf("user help content = %q", list.Content)
	}
	privateDetail, err := help.Handle(userCtx, command.Request{Prefix: "/", Args: "sec"})
	if err != nil {
		t.Fatalf("user private help: %v", err)
	}
	if privateDetail.Content != "unk command: sec" {
		t.Fatalf("private help content = %q", privateDetail.Content)
	}
	publicDetail, err := help.Handle(userCtx, command.Request{Prefix: "/", Args: "public"})
	if err != nil || !strings.Contains(publicDetail.Content, "/public") {
		t.Fatalf("public help = %#v, %v", publicDetail, err)
	}

	completer := help.(command.Completer)
	if got := completer.Complete(userCtx, command.CompletionRequest{Raw: "/help s", Prefix: "/", Name: "help", Args: "s", Cursor: len("/help s")}); len(got) != 0 {
		t.Fatalf("user private completions = %#v", got)
	}

	adminCtx := contextinfo.WithActor(context.Background(), contextinfo.Actor{Role: contextinfo.RoleSuperadmin})
	adminList, err := help.Handle(adminCtx, command.Request{Prefix: "/"})
	if err != nil || !strings.Contains(adminList.Content, "/secret") {
		t.Fatalf("admin help = %#v, %v", adminList, err)
	}
	adminDetail, err := help.Handle(adminCtx, command.Request{Prefix: "/", Args: "sec"})
	if err != nil || !strings.Contains(adminDetail.Content, "/secret") {
		t.Fatalf("admin private help = %#v, %v", adminDetail, err)
	}
	if got := completer.Complete(adminCtx, command.CompletionRequest{Raw: "/help s", Prefix: "/", Name: "help", Args: "s", Cursor: len("/help s")}); len(got) != 1 || got[0].Text != "secret" {
		t.Fatalf("admin private completions = %#v", got)
	}
}
