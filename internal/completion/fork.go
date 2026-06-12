package completion

import (
	"context"
	"strings"

	"elbot/internal/command"
	"elbot/internal/contextmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
)

const KindForkMessage = "fork_message"

type ForkMessageSource struct {
	Router   *command.Router
	Sessions *session.Service
	Store    storage.Store
	Scope    ScopeFunc
}

func (s ForkMessageSource) Complete(ctx context.Context, req Request) []Item {
	if s.Router == nil || s.Sessions == nil || s.Store == nil || s.Scope == nil {
		return nil
	}
	parsed := s.Router.Parse(req.Text)
	if !parsed.OK || parsed.Name != "fork" || !hasForkArgs(req.Text, parsed.Prefix) {
		return nil
	}
	sess, err := s.Sessions.Current(ctx, s.Scope(ctx))
	if err != nil {
		return nil
	}
	loaded, err := (contextmgr.Loader{Store: s.Store}).Load(ctx, sess.ID)
	if err != nil {
		return nil
	}
	prefix := strings.TrimSpace(parsed.Args)
	out := []Item{}
	for _, message := range loaded.Messages {
		if message.Role != storage.RoleAssistant || strings.TrimSpace(message.Content) == "" {
			continue
		}
		if strings.HasPrefix(message.ID, prefix) {
			text := parsed.Prefix + "fork " + message.ID
			out = append(out, Item{Text: text, Label: message.ID, Kind: KindForkMessage})
		}
	}
	return out
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
