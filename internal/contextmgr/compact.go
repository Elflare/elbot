package contextmgr

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

type PreparedCompact struct {
	Title string
	State *CompactState
}

func (s *Service) Compact(ctx context.Context, current *storage.Session, reason string, fallback modelmgr.Selection) (*PreparedCompact, error) {
	compactor, err := s.resolveCompactor(fallback.Protocol)
	if err != nil {
		return nil, err
	}
	return compactor.Prepare(ctx, current, reason, fallback)
}

// CheckCompaction verifies startup wiring without executing a compression.
func (s *Service) CheckCompaction(protocol llm.ProtocolID) error {
	_, err := s.resolveCompactor(protocol)
	return err
}

func (s *Service) resolveCompactor(protocol llm.ProtocolID) (Compactor, error) {
	if s.compactors == nil {
		return nil, fmt.Errorf("compaction routes are not configured")
	}
	return s.compactors.CompactorFor(protocol)
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
