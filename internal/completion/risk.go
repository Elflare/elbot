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
	parsed := s.Router.Parse(req.Text)
	if !parsed.OK || parsed.Args != "" {
		return nil, nil
	}
	current, err := s.Control.CurrentConfirmation(ctx, control.CurrentConfirmationRequest{})
	if err != nil {
		return nil, err
	}
	if current.Status != operationresult.Success || current.Data.Kind != "tool" {
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
