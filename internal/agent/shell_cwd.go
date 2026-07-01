package agent

import (
	"context"
	"strings"

	"elbot/internal/storage"
)

type sessionShellCWDStore struct {
	agent   *Agent
	session *storage.Session
}

func (s sessionShellCWDStore) GetShellCWD(ctx context.Context) (string, error) {
	if s.agent == nil || s.agent.store == nil || s.session == nil || s.session.ID == "" {
		return "", nil
	}
	latest, err := s.agent.store.Sessions().Get(ctx, s.session.ID)
	if err != nil {
		return "", err
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	return strings.TrimSpace(metadata.ShellCWD), nil
}

func (s sessionShellCWDStore) SetShellCWD(ctx context.Context, cwd string) error {
	cwd = strings.TrimSpace(cwd)
	if s.agent == nil || s.agent.store == nil || s.session == nil || s.session.ID == "" || cwd == "" {
		return nil
	}
	latest, err := s.agent.store.Sessions().Get(ctx, s.session.ID)
	if err != nil {
		return err
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	if metadata.ShellCWD == cwd {
		s.session.Metadata = latest.Metadata
		return nil
	}
	metadata.ShellCWD = cwd
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
