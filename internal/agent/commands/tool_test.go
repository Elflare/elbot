package commands

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/command"
	"elbot/internal/tool"
)

type fakeToolService struct {
	infos   []tool.Info
	removed []string
}

func (s *fakeToolService) List() []tool.Info            { return s.infos }
func (s *fakeToolService) Unregister(string) error      { return nil }
func (s *fakeToolService) Reload(context.Context) error { return nil }
func (s *fakeToolService) Remove(_ context.Context, name string) error {
	s.removed = append(s.removed, name)
	return nil
}

func TestToolsRemoveRequiresConfirm(t *testing.T) {
	tools := &fakeToolService{}
	h := NewTools(Deps{Tools: tools})
	result, err := h.Handle(context.Background(), command.Request{Args: "remove echo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "--confirm") || len(tools.removed) != 0 {
		t.Fatalf("result=%q removed=%#v", result.Content, tools.removed)
	}
}

func TestToolsRemoveConfirmedCallsRemove(t *testing.T) {
	tools := &fakeToolService{}
	h := NewTools(Deps{Tools: tools})
	result, err := h.Handle(context.Background(), command.Request{Args: "remove echo --confirm"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "removed skill: echo") || len(tools.removed) != 1 || tools.removed[0] != "echo" {
		t.Fatalf("result=%q removed=%#v", result.Content, tools.removed)
	}
}

func TestToolsCommandCompletesActionsNamesAndConfirm(t *testing.T) {
	tools := &fakeToolService{infos: []tool.Info{{Name: "echo", Description: "echo skill"}, {Name: "web_search", Description: "search"}}}
	cmd := NewTools(Deps{Tools: tools}).(command.Completer)

	got := cmd.Complete(context.Background(), command.CompletionRequest{Raw: "/tools r", Prefix: "/", Name: "tools", Args: "r", Cursor: len("/tools r")})
	if len(got) != 2 || got[0].Text != "reload" || got[1].Text != "remove" {
		t.Fatalf("action Complete = %#v", got)
	}

	got = cmd.Complete(context.Background(), command.CompletionRequest{Raw: "/tools remove e", Prefix: "/", Name: "tools", Args: "remove e", Cursor: len("/tools remove e")})
	if len(got) != 1 || got[0].Text != "echo" || got[0].Kind != "tool_name" {
		t.Fatalf("name Complete = %#v", got)
	}

	got = cmd.Complete(context.Background(), command.CompletionRequest{Raw: "/tools remove echo --", Prefix: "/", Name: "tools", Args: "remove echo --", Cursor: len("/tools remove echo --")})
	if len(got) != 1 || got[0].Text != "--confirm" {
		t.Fatalf("confirm Complete = %#v", got)
	}
}
