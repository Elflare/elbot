package fileops

import (
	"context"
	"fmt"

	"elbot/internal/workspace"
)

// PreparedRollback pins a backup for confirmation without retaining locks.
type PreparedRollback struct {
	// service and binding retain the operation's original authority.
	service *Service
	binding *fileBinding
	// rawPath is resolved again to detect workspace changes.
	rawPath string
	// preview contains the fixed backup ID, path, target and diff.
	preview RollbackPreview
}

func (s *Service) PrepareRollback(ctx context.Context, rawPath string) (*PreparedRollback, error) {
	binding, err := s.rollbackBinding(ctx)
	if err != nil {
		return nil, err
	}
	resolved, err := workspace.ResolveWorkspacePath(ctx, rawPath, workspace.PathResolveOptions{})
	if err != nil {
		return nil, err
	}
	preview, err := binding.session.Preview(ctx, resolved.Path, 0, s.CheckWrite)
	if err != nil {
		return nil, err
	}
	return &PreparedRollback{service: s, binding: binding, rawPath: rawPath, preview: preview}, nil
}

func (p *PreparedRollback) Preview() RollbackPreview { return p.preview }

func (p *PreparedRollback) Commit(ctx context.Context) (RollbackResult, error) {
	return p.service.rollback(ctx, p.rawPath, p.preview.ID, p)
}

func (s *Service) Rollback(ctx context.Context, rawPath string, expectedID uint64) (RollbackResult, error) {
	return s.rollback(ctx, rawPath, expectedID, nil)
}

func (s *Service) rollback(ctx context.Context, rawPath string, expectedID uint64, prepared *PreparedRollback) (RollbackResult, error) {
	binding, err := s.rollbackBinding(ctx)
	if err != nil {
		return RollbackResult{}, err
	}
	if prepared != nil {
		binding = prepared.binding
	}
	resolved, err := workspace.ResolveWorkspacePath(ctx, rawPath, workspace.PathResolveOptions{})
	if err != nil {
		return RollbackResult{}, err
	}
	if prepared != nil && resolved.Path != prepared.preview.Path {
		return RollbackResult{}, fmt.Errorf("rollback path changed after preflight")
	}
	guard := commitGuard{admission: binding.admission, validate: func(locked context.Context) error {
		latest, err := workspace.ResolveWorkspacePath(locked, rawPath, workspace.PathResolveOptions{})
		if err != nil {
			return err
		}
		if latest.Path != resolved.Path {
			return fmt.Errorf("rollback path changed before commit")
		}
		return nil
	}}
	return binding.session.rollback(ctx, resolved.Path, expectedID, s.CheckWrite, guard)
}
