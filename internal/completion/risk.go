package completion

import (
	"context"
	"strings"

	"elbot/internal/command"
	"elbot/internal/control"
	"elbot/internal/operationresult"
)

const KindRiskConfirmation = "risk_confirmation"

type RiskConfirmationSource struct {
	Router  *command.Router
	Control interface {
		CurrentConfirmation(context.Context, control.CurrentConfirmationRequest) (operationresult.Result[control.CurrentConfirmationResult], error)
	}
	CommandNames []string
}

func (s RiskConfirmationSource) Complete(ctx context.Context, req Request) ([]Item, error) {
	if s.Router == nil || s.Control == nil || len(s.CommandNames) == 0 {
		return nil, nil
	}
	current, err := s.Control.CurrentConfirmation(ctx, control.CurrentConfirmationRequest{})
	if err != nil || current.Status != operationresult.Success || current.Data.Kind != "tool" {
		return nil, nil
	}
	parsed := s.Router.Parse(req.Text)
	if !parsed.OK || hasAnyCommandArgs(req.Text, parsed.Prefix) {
		return nil, nil
	}
	out := []Item{}
	for _, name := range s.CommandNames {
		if strings.HasPrefix(name, parsed.Name) {
			out = append(out, Item{Text: parsed.Prefix + name, Label: name, Kind: KindRiskConfirmation})
		}
	}
	return out, nil
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
