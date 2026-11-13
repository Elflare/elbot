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
	if s.Router == nil || s.Control == nil {
		return nil, nil
	}
	parsed := s.Router.Parse(req.Text)
	if !parsed.OK || parsed.Name != "fork" || !hasForkArgs(req.Text, parsed.Prefix) {
		return nil, nil
	}
	prefix := strings.TrimSpace(parsed.Args)
	pages := [][]control.MessageData{}
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
		pages = append(pages, loaded.Data.Items)
		if !loaded.Data.HasNext {
			break
		}
	}
	out := []Item{}
	for i := len(pages) - 1; i >= 0; i-- {
		for _, message := range pages[i] {
			if message.Role != "assistant" || strings.TrimSpace(message.Content) == "" {
				continue
			}
			if strings.HasPrefix(message.ID, prefix) {
				text := parsed.Prefix + "fork " + message.ID
				out = append(out, Item{Text: text, Label: message.ID, Kind: KindForkMessage})
			}
		}
	}
	return out, nil
}

func hasForkArgs(text, prefix string) bool {
	text = strings.TrimLeft(text, " \t")
	if !strings.HasPrefix(text, prefix) {
		return false
	}
	rest := strings.TrimPrefix(text, prefix)
	_, args, ok := strings.Cut(rest, " ")
	return ok && strings.HasPrefix(strings.TrimLeft(rest, " \t"), "fork") && (args == "" || strings.TrimSpace(args) != "")
}
