package agent

import (
	"context"
	"strings"

	"elbot/internal/storage"
)

type sessionWorkspaceStore struct {
	agent   *Agent
	session *storage.Session
}

func (s sessionWorkspaceStore) GetWorkspaceDir(ctx context.Context) (string, error) {
	if s.agent == nil || s.agent.store == nil || s.session == nil || s.session.ID == "" {
		return "", nil
	}
	latest, err := s.agent.store.Sessions().Get(ctx, s.session.ID)
	if err != nil {
		return "", err
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	return strings.TrimSpace(metadata.WorkspaceDir), nil
}

func (s sessionWorkspaceStore) SetWorkspaceDir(ctx context.Context, dir string) error {
	dir = strings.TrimSpace(dir)
	if s.agent == nil || s.agent.store == nil || s.session == nil || s.session.ID == "" || dir == "" {
		return nil
	}
	latest, err := s.agent.store.Sessions().Get(ctx, s.session.ID)
	if err != nil {
		return err
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	if metadata.WorkspaceDir == dir {
		s.session.Metadata = latest.Metadata
		return nil
	}
	metadata.WorkspaceDir = dir
	return s.save(ctx, latest, metadata)
}

func (s sessionWorkspaceStore) ClearWorkspaceDir(ctx context.Context) error {
	if s.agent == nil || s.agent.store == nil || s.session == nil || s.session.ID == "" {
		return nil
	}
	latest, err := s.agent.store.Sessions().Get(ctx, s.session.ID)
	if err != nil {
		return err
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	if metadata.WorkspaceDir == "" {
		s.session.Metadata = latest.Metadata
		return nil
	}
	metadata.WorkspaceDir = ""
	return s.save(ctx, latest, metadata)
}

func (s sessionWorkspaceStore) save(ctx context.Context, latest *storage.Session, metadata sessionMetadata) error {
	encoded := encodeSessionMetadataInto(latest.Metadata, metadata)
	if encoded == latest.Metadata {
		s.session.Metadata = latest.Metadata
		return nil
	}
	latest.Metadata = encoded
	latest.UpdatedAt = storage.Now()
	if err := s.agent.store.Sessions().Update(ctx, latest); err != nil {
		return err
	}
	s.session.Metadata = encoded
	return nil
}
