package completion

import (
	"context"
	"testing"

	"elbot/internal/command"
)

type staticSource []Item

func (s staticSource) Complete(context.Context, Request) []Item { return s }

func TestServiceReturnsFirstNonEmptySource(t *testing.T) {
	svc := NewService(staticSource(nil), staticSource{{Text: "/model"}}, staticSource{{Text: "/models"}})
	got := svc.Complete(context.Background(), Request{Text: "/m"})
	if len(got) != 1 || got[0].Text != "/model" {
		t.Fatalf("Complete = %#v", got)
	}
}

func TestRouterSourceCompletesCommands(t *testing.T) {
	router := command.NewRouter([]string{"/"})
	if err := router.Register(command.NewFunc(command.Info{Name: "model", Aliases: []string{"m"}}, nil)); err != nil {
		t.Fatalf("register model: %v", err)
	}
	if err := router.Register(command.NewFunc(command.Info{Name: "models"}, nil)); err != nil {
		t.Fatalf("register models: %v", err)
	}
	items := RouterSource{Router: router}.Complete(context.Background(), Request{Text: "/m"})
	if len(items) != 3 || items[0].Text != "/model" || items[1].Text != "/m" || items[2].Text != "/models" {
		t.Fatalf("Complete = %#v", items)
	}
	for _, item := range items {
		if item.Kind != KindCommand {
			t.Fatalf("item kind = %q", item.Kind)
		}
	}
}

func TestTextsDropsEmptyItems(t *testing.T) {
	got := Texts([]Item{{Text: "/a"}, {}, {Text: "/b"}})
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Fatalf("Texts = %#v", got)
	}
}
