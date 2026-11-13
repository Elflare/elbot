package contextmgr

import (
	"context"
	"elbot/internal/operationresult"
	"fmt"
	"strings"

	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type PreparedCompact struct {
	Title                string
	State                *CompactState
	InitialMessage       string
	Seed                 *storage.NativeSeed
	ExpectedCheckpointID string
}

func (s *Service) Compact(ctx context.Context, current *storage.Session, reason string, fallback modelmgr.Selection) (operationresult.Result[*PreparedCompact], error) {
	origin, known, err := session.Origin(current)
	if err != nil {
		return operationresult.Result[*PreparedCompact]{}, err
	}
	if !known {
		return operationresult.Result[*PreparedCompact]{Status: operationresult.Rejected, Message: "source session origin is missing"}, nil
	}
	compactor, err := s.compactors.CompactorFor(origin)
	if err != nil {
		return operationresult.Result[*PreparedCompact]{}, err
	}
	return compactor.Prepare(ctx, current, reason, fallback)
}

func NextCompactedTitle(source *storage.Session, previous *CompactState) (title string, generation int, baseTitle string) {
	baseTitle = strings.TrimSpace(source.Title)
	if compact := previous; compact != nil && compact.Generation > 0 {
		generation = compact.Generation
		expected := formatCompactedTitle(compact.BaseTitle, compact.Generation)
		if source.Title == expected {
			baseTitle = compact.BaseTitle
		}
	}
	if baseTitle == "" {
		baseTitle = "New session"
	}
	generation++
	return formatCompactedTitle(baseTitle, generation), generation, baseTitle
}

func formatCompactedTitle(baseTitle string, generation int) string {
	return fmt.Sprintf("%s compacted-%d", strings.TrimSpace(baseTitle), generation)
}
