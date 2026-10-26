package builtin

import toolshare "elbot/internal/tool"

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/command"
)

type fakeToolService struct {
	infos   []toolshare.Info
	removed []string
	query   func(context.Context, toolshare.ToolQuery) ([]toolshare.Info, error)
}

func (s *fakeToolService) List() []toolshare.Info       { return s.infos }
func (s *fakeToolService) Unregister(string) error      { return nil }
func (s *fakeToolService) Reload(context.Context) error { return nil }
func (s *fakeToolService) Remove(_ context.Context, name string) error {
	s.removed = append(s.removed, name)
	return nil
}

func TestToolsRemoveRequiresConfirm(t *testing.T) {
	tools := &fakeToolService{}
	h := NewTools(Deps{Tools: tools, Skills: tools})
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
	h := NewTools(Deps{Tools: tools, Skills: tools})
	result, err := h.Handle(context.Background(), command.Request{Args: "remove echo --confirm"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "removed skill: echo") || len(tools.removed) != 1 || tools.removed[0] != "echo" {
		t.Fatalf("result=%q removed=%#v", result.Content, tools.removed)
	}
}

func TestToolsCommandCompletesActionsNamesAndConfirm(t *testing.T) {
	tools := &fakeToolService{infos: []toolshare.Info{{Name: "echo", Description: "echo skill"}, {Name: "web_search", Description: "search"}}}
	cmd := NewTools(Deps{Tools: tools, Skills: tools}).(command.Completer)

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

func (s *fakeToolService) QueryTools(ctx context.Context, query toolshare.ToolQuery) ([]toolshare.Info, error) {
	if s.query != nil {
		return s.query(ctx, query)
	}
	return s.infos, nil
}

func TestToolsListUsesQualifiedQueryAndReturnsQueryFailure(t *testing.T) {
	failure := errors.New("catalog unavailable")
	for _, fail := range []bool{false, true} {
		service := &fakeToolService{infos: []toolshare.Info{{Name: "unqualified"}}}
		service.query = func(ctx context.Context, query toolshare.ToolQuery) ([]toolshare.Info, error) {
			if !query.IncludeHidden || !query.IncludeDiscover || query.Kind != "" || query.Names != nil {
				t.Fatalf("query=%+v", query)
			}
			if fail {
				return nil, failure
			}
			return []toolshare.Info{{Name: "qualified", Hidden: true}}, nil
		}
		result, err := NewTools(Deps{Tools: service}).Handle(t.Context(), command.Request{})
		if fail {
			if !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
			continue
		}
		if err != nil || !strings.Contains(result.Content, "qualified") || strings.Contains(result.Content, "unqualified") {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}
