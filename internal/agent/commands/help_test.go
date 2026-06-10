package commands

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/command"
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

	result, err := NewHelp(deps).Handle(context.Background(), command.Request{Prefix: "/", Args: "aud"})
	if err != nil {
		t.Fatalf("help handle: %v", err)
	}
	for _, want := range []string{"command: audit", "usage: /audit [options]", "aliases: aud", "--event <name>"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("help content missing %q: %q", want, result.Content)
		}
	}
}
