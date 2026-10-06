package session

import (
	"context"
	"fmt"

	"elbot/internal/llm"
	"elbot/internal/storage"
)

type PreparedMaterial struct {
	Seed                 *storage.NativeSeed
	ExpectedCheckpointID string
}

type MaterialPreparer interface {
	PrepareFork(context.Context, *storage.Session, *storage.Message) (*PreparedMaterial, error)
	PrepareCopy(context.Context, *storage.Session) (*PreparedMaterial, error)
}

type MaterialResolver interface {
	MaterialFor(llm.Origin) (MaterialPreparer, error)
}

// DisplayMaterial needs no native root. Its use is chosen at composition, never
// by a protocol branch inside Session's lifecycle implementation.
type DisplayMaterial struct{}

func (DisplayMaterial) PrepareFork(context.Context, *storage.Session, *storage.Message) (*PreparedMaterial, error) {
	return nil, nil
}
func (DisplayMaterial) PrepareCopy(context.Context, *storage.Session) (*PreparedMaterial, error) {
	return nil, nil
}

func (s *Service) SetMaterials(resolver MaterialResolver) { s.materials = resolver }

func (s *Service) prepareMaterial(ctx context.Context, source *storage.Session, message *storage.Message) (*PreparedMaterial, error) {
	origin, known, err := Origin(source)
	if err != nil || !known {
		return nil, err
	}
	if s.materials == nil {
		return nil, fmt.Errorf("source session material capabilities are not configured")
	}
	preparer, err := s.materials.MaterialFor(origin)
	if err != nil {
		return nil, err
	}
	if message != nil {
		return preparer.PrepareFork(ctx, source, message)
	}
	return preparer.PrepareCopy(ctx, source)
}

func materialCreate(row *storage.Session, source *storage.Session, prepared *PreparedMaterial, messages []*storage.Message) storage.SessionMaterialCreate {
	req := storage.SessionMaterialCreate{Session: row, Messages: messages}
	if prepared != nil {
		req.Seed, req.SourceSessionID, req.ExpectedCheckpointID = prepared.Seed, source.ID, prepared.ExpectedCheckpointID
	}
	return req
}

func (s *Service) currentBinding(scope Scope) *Binding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current[scope.Key()]
}
