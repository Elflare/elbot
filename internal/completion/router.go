package completion

import (
	"context"
	"strings"

	"elbot/internal/command"
	"elbot/internal/security"
)

const KindCommand = "command"

type RouterSource struct {
	Router *command.Router
	Actor  func(context.Context) security.Actor
}

func (s RouterSource) completeArgs(ctx context.Context, req Request, actor security.Actor) []Item {
	parsed := s.Router.Parse(req.Text)
	if !parsed.OK || parsed.Name == "" || !strings.ContainsAny(strings.TrimPrefix(strings.TrimLeft(req.Text, " \t"), parsed.Prefix), " \t") {
		return nil
	}
	h, ok := s.Router.Handler(parsed.Name)
	if !ok {
		return nil
	}
	if !command.CanAccess(h.Info(), actor) {
		return nil
	}
	completer, ok := h.(command.Completer)
	if !ok {
		return nil
	}
	items := completer.Complete(ctx, command.CompletionRequest{Raw: req.Text, Prefix: parsed.Prefix, Name: parsed.Name, Args: parsed.Args, Cursor: req.CursorOrEnd()})
	out := make([]Item, 0, len(items))
	for _, item := range items {
		out = append(out, Item{Text: item.Text, Label: item.Label, Description: item.Description, Kind: item.Kind, ReplaceStart: item.ReplaceStart, ReplaceEnd: item.ReplaceEnd})
	}
	return out
}

func (s RouterSource) actor(ctx context.Context) security.Actor {
	if s.Actor != nil {
		return s.Actor(ctx)
	}
	actor, _ := security.ActorFromContext(ctx)
	return actor
}
func (s RouterSource) Complete(ctx context.Context, req Request) []Item {
	if s.Router == nil {
		return nil
	}
	actor := s.actor(ctx)
	if items := s.completeArgs(ctx, req, actor); len(items) > 0 {
		return items
	}
	texts := s.Router.CompleteForActor(req.Text, actor)
	if len(texts) == 0 {
		return nil
	}
	items := make([]Item, 0, len(texts))
	for _, text := range texts {
		items = append(items, Item{Text: text, Label: strings.TrimLeft(text, "/-"), Kind: KindCommand})
	}
	return items

}
