package commands

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/command"
	"elbot/internal/tool"
)

type fakeToolService struct {
	removed []string
}

func (s *fakeToolService) List() []tool.Info            { return nil }
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
