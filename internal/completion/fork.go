package completion

import (
	"context"
	"strings"

	"elbot/internal/command"
	"elbot/internal/control"
	"elbot/internal/operationresult"
)

const KindForkMessage = "fork_message"

type ForkMessageSource struct {
	Router  *command.Router
	Control interface {
		SessionMessages(context.Context, control.SessionMessagesRequest) (operationresult.Result[control.SessionMessagesResult], error)
	}
	PageSize int
}

func (s ForkMessageSource) Complete(ctx context.Context, req Request) ([]Item, error) {
	parsed := s.Router.Parse(req.Text)
	if !parsed.OK || parsed.Name != "fork" || !hasForkArgs(req.Text, parsed.Prefix) {
		return nil, nil
	}
	pages := [][]Item{}
	sessionID := ""
	for page := 1; ; page++ {
		loaded, err := s.Control.SessionMessages(ctx, control.SessionMessagesRequest{SessionID: sessionID, Page: page, PageSize: s.PageSize})
		if err != nil {
			return nil, err
		}
		if loaded.Status != operationresult.Success {
			return nil, nil
		}
		sessionID = loaded.Data.SessionID
		var items []Item
		for _, message := range loaded.Data.Items {
			if message.Role != "assistant" || strings.TrimSpace(message.Content) == "" {
				continue
			}
			if strings.HasPrefix(message.ID, parsed.Args) {
				text := parsed.Prefix + "fork " + message.ID
				items = append(items, Item{Text: text, Label: message.ID, Kind: KindForkMessage})
			}
		}
		pages = append(pages, items)
		if !loaded.Data.HasNext {
			break
		}
	}
	out := []Item{}
	for i := len(pages) - 1; i >= 0; i-- {
		out = append(out, pages[i]...)
	}
	return out, nil
}

func hasForkArgs(text, prefix string) bool {
	text = strings.TrimLeft(text, " \t")
	rest := strings.TrimPrefix(text, prefix)
	_, args, ok := strings.Cut(rest, " ")
	return ok && (args == "" || strings.TrimSpace(args) != "")
}
