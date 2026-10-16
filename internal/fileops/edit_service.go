package fileops

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"elbot/internal/workspace"
)

type EditRequest struct {
	Path             string
	Encoding         string
	ExpectedRevision string
	Create           bool
	ContextLines     int
	Edits            []Edit
	Options          EditFileOptions
}

// PreparedEdit holds one confirmation snapshot without holding a target lock.
type PreparedEdit struct {
	// service and binding retain the original operation authority.
	service *Service
	binding *fileBinding
	// request is an independent copy of the approved edit arguments.
	request EditRequest
	// resolved and target pin the workspace path and actual filesystem target.
	resolved workspace.ResolvedPath
	target   string
	// result contains the original revision and confirmation diff.
	result EditResult
}

func cloneEditRequest(req EditRequest) EditRequest {
	req.Edits = append([]Edit(nil), req.Edits...)
	for i := range req.Edits {
		if req.Edits[i].NewText != nil {
			req.Edits[i].NewText = new(*req.Edits[i].NewText)
		}
		if req.Edits[i].Index != nil {
			req.Edits[i].Index = new(*req.Edits[i].Index)
		}
	}
	return req
}

func (p *PreparedEdit) Preview() EditResult {
	result := p.result
	result.NewBytes = bytes.Clone(result.NewBytes)
	return result
}

func (p *PreparedEdit) Commit(ctx context.Context) (EditResult, workspace.ResolvedPath, error) {
	return p.service.edit(ctx, p.request, p)
}

func (p *PreparedEdit) check(file File, created bool) error {
	if p.result.Created != created || p.result.RevisionBefore != ContentRevision(file.Bytes) {
		return fmt.Errorf("file changed after preflight")
	}
	return nil
}

func (s *Service) editPath(ctx context.Context, req EditRequest) (workspace.ResolvedPath, string, error) {
	resolved, err := workspace.ResolveWorkspacePath(ctx, req.Path, workspace.PathResolveOptions{AllowCreate: req.Create})
	if err != nil {
		return resolved, "", err
	}
	target, err := ResolveFileTarget(resolved.Path, req.Create)
	if err != nil {
		return resolved, "", err
	}
	if s.CheckWrite != nil {
		for _, path := range []string{resolved.Path, target} {
			if err := s.CheckWrite(path); err != nil {
				return resolved, "", err
			}
		}
	}
	return resolved, target, nil
}

func (s *Service) PrepareEdit(ctx context.Context, req EditRequest) (*PreparedEdit, error) {
	binding, lease, err := s.editBinding(ctx)
	if err != nil {
		return nil, err
	}
	if binding != nil {
		binding = &fileBinding{service: s, session: lease, admission: binding.admission}
	}
	req = cloneEditRequest(req)
	resolved, target, err := s.editPath(ctx, req)
	if err != nil {
		return nil, err
	}
	unlock, err := s.Manager.lockTarget(ctx, target)
	if err != nil {
		return nil, err
	}
	defer unlock()
	result, err := EditFileWithOptions(resolved.Path, req.Encoding, req.ExpectedRevision, req.Create, true, req.ContextLines, req.Edits, req.Options)
	if err != nil {
		return nil, fmt.Errorf("preflight edit_file: %w", err)
	}
	if err := checkFileTarget(resolved.Path, target, result.Created); err != nil {
		return nil, err
	}
	return &PreparedEdit{service: s, binding: binding, request: req, resolved: resolved, target: target, result: result}, nil
}

// Edit directly commits an edit without building a confirmation preview.
func (s *Service) Edit(ctx context.Context, req EditRequest) (EditResult, workspace.ResolvedPath, error) {
	return s.edit(ctx, req, nil)
}

func (s *Service) edit(ctx context.Context, req EditRequest, prepared *PreparedEdit) (EditResult, workspace.ResolvedPath, error) {
	binding, lease, err := s.editBinding(ctx)
	if err != nil {
		return EditResult{}, workspace.ResolvedPath{}, err
	}
	if prepared != nil {
		binding = prepared.binding
		lease = nil
		if binding != nil {
			lease = binding.session
		}
	}
	resolved, target, err := s.editPath(ctx, req)
	if err != nil {
		return EditResult{}, resolved, err
	}
	if prepared != nil && (prepared.resolved.Path != resolved.Path || prepared.target != target) {
		return EditResult{}, resolved, fmt.Errorf("edit target changed after preflight")
	}
	guard := commitGuard{validate: func(locked context.Context) error {
		latest, currentTarget, err := s.editPath(locked, req)
		if err != nil {
			return err
		}
		if latest.Path != resolved.Path || currentTarget != target {
			return fmt.Errorf("edit target changed before commit")
		}
		return nil
	}}
	if binding != nil {
		guard.admission = binding.admission
	}
	options := req.Options
	if prepared != nil {
		options.beforeWrite = func(file File, created bool, _ os.FileMode) error {
			return prepared.check(file, created)
		}
	}
	result, err := s.Manager.editFile(ctx, lease, resolved.Path, req.Encoding, req.ExpectedRevision, req.Create, req.ContextLines, req.Edits, options, s.CheckWrite, guard)
	return result, resolved, err
}
