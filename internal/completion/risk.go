package completion

import (
	"context"
	"strings"

	"elbot/internal/command"
	"elbot/internal/session"
	"elbot/internal/turn"
)

const KindRiskConfirmation = "risk_confirmation"

type ScopeFunc func(context.Context) session.Scope

type RiskConfirmationSource struct {
	Router       *command.Router
	Sessions     *session.Service
	Turns        *turn.Manager
	Scope        ScopeFunc
	CommandNames []string
}

func (s RiskConfirmationSource) Complete(ctx context.Context, req Request) []Item {
	if s.Router == nil || s.Sessions == nil || s.Turns == nil || s.Scope == nil || len(s.CommandNames) == 0 {
		return nil
	}
	sess, err := s.Sessions.Current(ctx, s.Scope(ctx))
	if err != nil || s.Turns.Snapshot(sess.ID).Phase != turn.PhaseAwaitRiskConfirm {
		return nil
	}
	parsed := s.Router.Parse(req.Text)
	if !parsed.OK || hasAnyCommandArgs(req.Text, parsed.Prefix) {
		return nil
	}
	out := []Item{}
	for _, name := range s.CommandNames {
		if strings.HasPrefix(name, parsed.Name) {
			out = append(out, Item{Text: parsed.Prefix + name, Label: name, Kind: KindRiskConfirmation})
		}
	}
	return out
}

func hasAnyCommandArgs(text, prefix string) bool {
	text = strings.TrimLeft(text, " \t")
	if !strings.HasPrefix(text, prefix) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, prefix))
	_, args, ok := strings.Cut(rest, " ")
	return ok && strings.TrimSpace(args) != ""
}
