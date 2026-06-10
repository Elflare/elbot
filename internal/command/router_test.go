package command

import (
	"context"
	"strings"
	"testing"
)

func TestRouterDispatchAndHelpInfo(t *testing.T) {
	r := NewRouter([]string{"/", "-"})
	called := false
	if err := r.Register(NewFunc(Info{
		Name:        "ping",
		Usage:       "/ping [text]",
		Description: "Ping command.",
		Aliases:     []string{"p"},
	}, func(ctx context.Context, req Request) (*Result, error) {
		called = true
		if req.Prefix != "-" || req.Name != "ping" || req.Args != "hello" {
			t.Fatalf("request = %#v", req)
		}
		return &Result{Content: "pong"}, nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	result, err := r.Dispatch(context.Background(), "-ping hello")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !called || result.Content != "pong" {
		t.Fatalf("called=%v result=%#v", called, result)
	}

	if len(r.Commands()) != 1 || r.Commands()[0].Name != "ping" {
		t.Fatalf("Commands = %#v", r.Commands())
	}
}

func TestRouterRejectsDuplicateAlias(t *testing.T) {
	r := NewRouter([]string{"/"})
	if err := r.Register(NewFunc(Info{Name: "one", Aliases: []string{"x"}}, nil)); err != nil {
		t.Fatalf("Register one: %v", err)
	}
	if err := r.Register(NewFunc(Info{Name: "two", Aliases: []string{"x"}}, nil)); err == nil {
		t.Fatal("expected duplicate alias error")
	}
}

func TestRouterCommandInfoFindsPrimaryAndAlias(t *testing.T) {
	r := NewRouter([]string{"/"})
	if err := r.Register(NewFunc(Info{Name: "audit", Usage: "/audit", Aliases: []string{"aud"}, Help: "details"}, nil)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, name := range []string{"audit", "aud", "/audit"} {
		info, ok := r.CommandInfo(name)
		if !ok {
			t.Fatalf("CommandInfo(%q) not found", name)
		}
		if info.Name != "audit" || info.Help != "details" {
			t.Fatalf("CommandInfo(%q) = %#v", name, info)
		}
	}
}

func TestRouterUnknownCommand(t *testing.T) {
	r := NewRouter([]string{"/"})
	result, err := r.Dispatch(context.Background(), "/missing")
	if err != nil {
		t.Fatalf("Dispatch unknown: %v", err)
	}
	if !strings.Contains(result.Content, "unknown command: /missing") {
		t.Fatalf("unexpected content: %q", result.Content)
	}
}

func TestRouterLongestPrefixWins(t *testing.T) {
	r := NewRouter([]string{"/", "//"})
	parsed := r.Parse("//help")
	if !parsed.OK || parsed.Prefix != "//" || parsed.Name != "help" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestRouterCompleteCommands(t *testing.T) {
	r := NewRouter([]string{"/", "-"})
	if err := r.Register(NewFunc(Info{Name: "model", Aliases: []string{"m"}}, nil)); err != nil {
		t.Fatalf("Register model: %v", err)
	}
	if err := r.Register(NewFunc(Info{Name: "models"}, nil)); err != nil {
		t.Fatalf("Register models: %v", err)
	}
	if err := r.Register(NewFunc(Info{Name: "resume"}, nil)); err != nil {
		t.Fatalf("Register resume: %v", err)
	}

	assertComplete(t, r.Complete("/m"), []string{"/model", "/m", "/models"})
	assertComplete(t, r.Complete("-res"), []string{"-resume"})
	assertComplete(t, r.Complete("/model "), nil)
	assertComplete(t, r.Complete("hello"), nil)
}

func assertComplete(t *testing.T, got []string, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("completions = %#v, want %#v", got, want)
	}
}
