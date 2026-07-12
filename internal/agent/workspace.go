package agent

import (
	"context"
	"slices"
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
	return s.SetWorkspaceDirWithAgentNotice(ctx, dir, false)
}

func (s sessionWorkspaceStore) ClearWorkspaceDir(ctx context.Context) error {
	return s.ClearWorkspaceDirWithAgentNotice(ctx, "", false)
}

func (s sessionWorkspaceStore) HasWorkspaceAgentNoticeDir(ctx context.Context, dir string) (bool, error) {
	dir = strings.TrimSpace(dir)
	if s.agent == nil || s.agent.store == nil || s.session == nil || s.session.ID == "" || dir == "" {
		return false, nil
	}
	latest, err := s.agent.store.Sessions().Get(ctx, s.session.ID)
	if err != nil {
		return false, err
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	s.session.Metadata = latest.Metadata
	return slices.Contains(metadata.WorkspaceAgentNoticeDirs, dir), nil
}

func (s sessionWorkspaceStore) MarkWorkspaceAgentNoticeDir(ctx context.Context, dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	if s.agent == nil || s.agent.store == nil || s.session == nil || s.session.ID == "" {
		return nil
	}
	latest, err := s.agent.store.Sessions().Get(ctx, s.session.ID)
	if err != nil {
		return err
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	if slices.Contains(metadata.WorkspaceAgentNoticeDirs, dir) {
		s.session.Metadata = latest.Metadata
		return nil
	}
	metadata.WorkspaceAgentNoticeDirs = append(metadata.WorkspaceAgentNoticeDirs, dir)
	return s.save(ctx, latest, metadata)
}

func (s sessionWorkspaceStore) SetWorkspaceDirWithAgentNotice(ctx context.Context, dir string, markNotice bool) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	return s.saveWorkspaceDirWithAgentNotice(ctx, dir, dir, markNotice)
}

func (s sessionWorkspaceStore) ClearWorkspaceDirWithAgentNotice(ctx context.Context, dir string, markNotice bool) error {
	return s.saveWorkspaceDirWithAgentNotice(ctx, "", strings.TrimSpace(dir), markNotice)
}

func (s sessionWorkspaceStore) saveWorkspaceDirWithAgentNotice(ctx context.Context, workspaceDir, noticeDir string, markNotice bool) error {
	if s.agent == nil || s.agent.store == nil || s.session == nil || s.session.ID == "" {
		return nil
	}
	latest, err := s.agent.store.Sessions().Get(ctx, s.session.ID)
	if err != nil {
		return err
	}
	metadata := decodeSessionMetadata(latest.Metadata)
	changed := metadata.WorkspaceDir != workspaceDir
	metadata.WorkspaceDir = workspaceDir
	if markNotice && noticeDir != "" && !slices.Contains(metadata.WorkspaceAgentNoticeDirs, noticeDir) {
		metadata.WorkspaceAgentNoticeDirs = append(metadata.WorkspaceAgentNoticeDirs, noticeDir)
		changed = true
	}
	if !changed {
		s.session.Metadata = latest.Metadata
		return nil
	}
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
