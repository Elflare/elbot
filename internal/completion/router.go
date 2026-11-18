package completion

import (
	"context"
	"strings"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
)

const KindCommand = "command"

type RouterSource struct {
	Router *command.Router
}

func (s RouterSource) completeArgs(ctx context.Context, req Request, actor contextinfo.Actor) []Item {
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

func (s RouterSource) Complete(ctx context.Context, req Request) ([]Item, error) {
	actor, _ := contextinfo.ActorFromContext(ctx)
	if items := s.completeArgs(ctx, req, actor); len(items) > 0 {
		return items, nil
	}
	texts := s.Router.CompleteForActor(req.Text, actor)
	items := make([]Item, 0, len(texts))
	for _, text := range texts {
		items = append(items, Item{Text: text, Label: strings.TrimLeft(text, "/-"), Kind: KindCommand})
	}
	return items, nil
}
