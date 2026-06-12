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

func (s RouterSource) Complete(ctx context.Context, req Request) []Item {
	_ = ctx
	if s.Router == nil {
		return nil
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
