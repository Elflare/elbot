package commands

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/command"
	"elbot/internal/security"
)

func TestHelpCommandShowsDetailedHelp(t *testing.T) {
	router := command.NewRouter([]string{"/"})
	deps := Deps{Router: router}
	if err := RegisterFactories(router, deps, NewHelp); err != nil {
		t.Fatalf("register help: %v", err)
	}
	if err := router.Register(command.NewFunc(command.Info{
		Name:        "audit",
		Usage:       "/audit [options]",
		Description: "Show audit events.",
		Aliases:     []string{"aud"},
		Help:        "Options:\n  --event <name>",
	}, nil)); err != nil {
		t.Fatalf("register audit: %v", err)
	}

	ctx := security.WithActor(context.Background(), security.Actor{Role: security.RoleSuperadmin})
	result, err := NewHelp(deps).Handle(ctx, command.Request{Prefix: "/", Args: "aud"})
	if err != nil {
		t.Fatalf("help handle: %v", err)
	}
	for _, want := range []string{"command: audit", "usage: /audit [options]", "aliases: aud", "--event <name>"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("help content missing %q: %q", want, result.Content)
		}
	}
}

func TestHelpCommandFiltersByActor(t *testing.T) {
	router := command.NewRouter([]string{"/"})
	deps := Deps{Router: router}
	if err := RegisterFactories(router, deps, NewHelp); err != nil {
		t.Fatalf("register help: %v", err)
	}
	if err := router.Register(command.NewFunc(command.Info{Name: "public", Usage: "/public", Description: "Public command.", MinRole: security.RoleUser}, nil)); err != nil {
		t.Fatalf("register public: %v", err)
	}
	if err := router.Register(command.NewFunc(command.Info{Name: "secret", Usage: "/secret", Description: "Private command.", Aliases: []string{"sec"}}, nil)); err != nil {
		t.Fatalf("register secret: %v", err)
	}

	help := NewHelp(deps)
	userCtx := security.WithActor(context.Background(), security.Actor{Role: security.RoleUser})
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
	if privateDetail.Content != "unknown command: sec" {
		t.Fatalf("private help content = %q", privateDetail.Content)
	}
	publicDetail, err := help.Handle(userCtx, command.Request{Prefix: "/", Args: "public"})
	if err != nil || !strings.Contains(publicDetail.Content, "command: public") {
		t.Fatalf("public help = %#v, %v", publicDetail, err)
	}

	completer := help.(command.Completer)
	if got := completer.Complete(userCtx, command.CompletionRequest{Raw: "/help s", Prefix: "/", Name: "help", Args: "s", Cursor: len("/help s")}); len(got) != 0 {
		t.Fatalf("user private completions = %#v", got)
	}

	adminCtx := security.WithActor(context.Background(), security.Actor{Role: security.RoleSuperadmin})
	adminList, err := help.Handle(adminCtx, command.Request{Prefix: "/"})
	if err != nil || !strings.Contains(adminList.Content, "/secret") {
		t.Fatalf("admin help = %#v, %v", adminList, err)
	}
	adminDetail, err := help.Handle(adminCtx, command.Request{Prefix: "/", Args: "sec"})
	if err != nil || !strings.Contains(adminDetail.Content, "command: secret") {
		t.Fatalf("admin private help = %#v, %v", adminDetail, err)
	}
	if got := completer.Complete(adminCtx, command.CompletionRequest{Raw: "/help s", Prefix: "/", Name: "help", Args: "s", Cursor: len("/help s")}); len(got) != 1 || got[0].Text != "secret" {
		t.Fatalf("admin private completions = %#v", got)
	}
}
