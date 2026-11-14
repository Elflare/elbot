package fileops

import (
	"context"

	"elbot/internal/operationresult"
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

func (s *Service) PrepareRollback(ctx context.Context, rawPath string) (operationresult.Result[*PreparedRollback], error) {
	binding := s.rollbackBinding(ctx)
	if binding.Status != operationresult.Success {
		return operationresult.Result[*PreparedRollback]{Status: binding.Status, Message: binding.Message}, nil
	}
	resolved, err := workspace.ResolveWorkspacePath(ctx, rawPath, workspace.PathResolveOptions{})
	if err != nil {
		return operationresult.Result[*PreparedRollback]{}, err
	}
	if resolved.Status != operationresult.Success {
		return operationresult.Result[*PreparedRollback]{Status: resolved.Status, Message: resolved.Message}, nil
	}
	preview, err := binding.Data.session.Preview(ctx, resolved.Data.Path, 0, s.CheckWrite)
	if err != nil {
		return operationresult.Result[*PreparedRollback]{}, err
	}
	if preview.Status != operationresult.Success {
		return operationresult.Result[*PreparedRollback]{Status: preview.Status, Message: preview.Message}, nil
	}
	return operationresult.Result[*PreparedRollback]{Status: operationresult.Success, Data: &PreparedRollback{service: s, binding: binding.Data, rawPath: rawPath, preview: preview.Data}}, nil
}

func (p *PreparedRollback) Preview() RollbackPreview { return p.preview }

func (p *PreparedRollback) Commit(ctx context.Context) (operationresult.Result[RollbackResult], error) {
	return p.service.rollback(ctx, p.rawPath, p.preview.ID, p)
}

func (s *Service) Rollback(ctx context.Context, rawPath string, expectedID uint64) (operationresult.Result[RollbackResult], error) {
	return s.rollback(ctx, rawPath, expectedID, nil)
}

func (s *Service) rollback(ctx context.Context, rawPath string, expectedID uint64, prepared *PreparedRollback) (operationresult.Result[RollbackResult], error) {
	binding := s.rollbackBinding(ctx)
	if binding.Status != operationresult.Success {
		return operationresult.Result[RollbackResult]{Status: binding.Status, Message: binding.Message}, nil
	}
	if prepared != nil {
		binding.Data = prepared.binding
	}
	resolved, err := workspace.ResolveWorkspacePath(ctx, rawPath, workspace.PathResolveOptions{})
	if err != nil {
		return operationresult.Result[RollbackResult]{}, err
	}
	if resolved.Status != operationresult.Success {
		return operationresult.Result[RollbackResult]{Status: resolved.Status, Message: resolved.Message}, nil
	}
	if prepared != nil && resolved.Data.Path != prepared.preview.Path {
		return operationresult.Result[RollbackResult]{Status: operationresult.Conflict, Message: "rollback path changed after preflight"}, nil
	}
	guard := commitGuard{admission: binding.Data.admission, validate: func(locked context.Context) (operationresult.Result[struct{}], error) {
		latest, err := workspace.ResolveWorkspacePath(locked, rawPath, workspace.PathResolveOptions{})
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		if latest.Status != operationresult.Success {
			return operationresult.Result[struct{}]{Status: latest.Status, Message: latest.Message}, nil
		}
		if latest.Data.Path != resolved.Data.Path {
			return operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "rollback path changed before commit"}, nil
		}
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}}
	return binding.Data.session.rollback(ctx, resolved.Data.Path, expectedID, s.CheckWrite, guard)
}
