package completion

import (
	"context"
	"strings"

	"elbot/internal/command"
)

const KindCommand = "command"

type RouterSource struct {
	Router *command.Router
}

func (s RouterSource) completeArgs(ctx context.Context, req Request) []Item {
	parsed := s.Router.Parse(req.Text)
	if !parsed.OK || parsed.Name == "" || !strings.ContainsAny(strings.TrimPrefix(strings.TrimLeft(req.Text, " \t"), parsed.Prefix), " \t") {
		return nil
	}
	h, ok := s.Router.Handler(parsed.Name)
	if !ok {
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

func (s RouterSource) Complete(ctx context.Context, req Request) []Item {
	_ = ctx
	if s.Router == nil {
		return nil
	}
	if items := s.completeArgs(ctx, req); len(items) > 0 {
		return items
	}
	texts := s.Router.Complete(req.Text)
	if len(texts) == 0 {
		return nil
	}
	items := make([]Item, 0, len(texts))
	for _, text := range texts {
		items = append(items, Item{Text: text, Label: strings.TrimLeft(text, "/-"), Kind: KindCommand})
	}
	return items

}
