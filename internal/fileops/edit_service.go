package fileops

import (
	"context"
	"fmt"

	"elbot/internal/operationresult"
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
	// resolved pins the workspace path; file holds the shared file snapshot.
	resolved workspace.ResolvedPath
	file     *PreparedFile
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

// Preview returns confirmation metadata without exposing the prepared bytes.
func (p *PreparedEdit) Preview() EditResult {
	return p.file.Preview()
}

func (p *PreparedEdit) Commit(ctx context.Context) (operationresult.Result[EditResult], workspace.ResolvedPath, error) {
	return p.service.edit(ctx, p.request, p)
}

func (s *Service) editPath(ctx context.Context, req EditRequest) (workspace.ResolvedPath, string, operationresult.Result[struct{}], error) {
	path, err := workspace.ResolveWorkspacePath(ctx, req.Path, workspace.PathResolveOptions{AllowCreate: req.Create})
	if err != nil {
		return workspace.ResolvedPath{}, "", operationresult.Result[struct{}]{}, err
	}
	if path.Status != operationresult.Success {
		return workspace.ResolvedPath{}, "", operationresult.Result[struct{}]{Status: path.Status, Message: path.Message}, nil
	}
	resolved := path.Data
	selected, err := ResolveFileTarget(resolved.Path, req.Create)
	if err != nil {
		return resolved, "", operationresult.Result[struct{}]{}, err
	}
	if selected.Status != operationresult.Success {
		return resolved, "", operationresult.Result[struct{}]{Status: selected.Status, Message: selected.Message}, nil
	}
	target := selected.Data
	return resolved, target, operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (s *Service) PrepareEdit(ctx context.Context, req EditRequest) (operationresult.Result[*PreparedEdit], error) {
	captured, lease, err := s.editBinding(ctx)
	if err != nil {
		return operationresult.Result[*PreparedEdit]{}, err
	}
	if captured.Status != operationresult.Success {
		return operationresult.Result[*PreparedEdit]{Status: captured.Status, Message: captured.Message}, nil
	}
	binding := captured.Data
	if binding != nil {
		binding = &fileBinding{service: s, session: lease, admission: binding.admission}
	}
	req = cloneEditRequest(req)
	resolved, target, checked, err := s.editPath(ctx, req)
	if err != nil {
		return operationresult.Result[*PreparedEdit]{}, err
	}
	if checked.Status != operationresult.Success {
		return operationresult.Result[*PreparedEdit]{Status: checked.Status, Message: checked.Message}, nil
	}
	if s.CheckWrite != nil {
		for _, path := range []string{resolved.Path, target} {
			checked := s.CheckWrite(path)
			if checked.Status != operationresult.Success {
				return operationresult.Result[*PreparedEdit]{Status: checked.Status, Message: checked.Message}, nil
			}
		}
	}
	unlock, err := s.manager.lockTarget(ctx, target)
	if err != nil {
		return operationresult.Result[*PreparedEdit]{}, err
	}
	defer unlock()
	fileRequest := req
	fileRequest.Path = resolved.Path
	prepared, err := PrepareFileEdit(ctx, fileRequest, nil)
	if err != nil {
		return operationresult.Result[*PreparedEdit]{}, fmt.Errorf("preflight edit_file: %w", err)
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[*PreparedEdit]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	file := prepared.Data
	if file.target != target {
		return operationresult.Result[*PreparedEdit]{Status: operationresult.Conflict, Message: "edit target changed during preparation"}, nil
	}
	return operationresult.Result[*PreparedEdit]{Status: operationresult.Success, Data: &PreparedEdit{service: s, binding: binding, request: req, resolved: resolved, file: file}}, nil
}

// Edit directly commits an edit without building a confirmation preview.
func (s *Service) Edit(ctx context.Context, req EditRequest) (operationresult.Result[EditResult], workspace.ResolvedPath, error) {
	return s.edit(ctx, req, nil)
}

func (s *Service) edit(ctx context.Context, req EditRequest, prepared *PreparedEdit) (operationresult.Result[EditResult], workspace.ResolvedPath, error) {
	captured, lease, err := s.editBinding(ctx)
	if err != nil {
		return operationresult.Result[EditResult]{}, workspace.ResolvedPath{}, err
	}
	if captured.Status != operationresult.Success {
		return operationresult.Result[EditResult]{Status: captured.Status, Message: captured.Message}, workspace.ResolvedPath{}, nil
	}
	binding := captured.Data
	if prepared != nil {
		binding = prepared.binding
		lease = nil
		if binding != nil {
			lease = binding.session
		}
	}
	resolved, target, checked, err := s.editPath(ctx, req)
	if err != nil {
		return operationresult.Result[EditResult]{}, resolved, err
	}
	if checked.Status != operationresult.Success {
		return operationresult.Result[EditResult]{Status: checked.Status, Message: checked.Message}, resolved, nil
	}
	if prepared != nil && (prepared.resolved.Path != resolved.Path || prepared.file.target != target) {
		return operationresult.Result[EditResult]{Status: operationresult.Conflict, Message: "edit target changed after preflight"}, resolved, nil
	}
	// Reject before creating parent directories; Manager rechecks at commit.
	if s.CheckWrite != nil {
		for _, path := range []string{resolved.Path, target} {
			checked := s.CheckWrite(path)
			if checked.Status != operationresult.Success {
				return operationresult.Result[EditResult]{Status: checked.Status, Message: checked.Message}, resolved, nil
			}
		}
	}
	guard := commitGuard{validate: func(locked context.Context) (operationresult.Result[struct{}], error) {
		latest, currentTarget, checked, err := s.editPath(locked, req)
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		if checked.Status != operationresult.Success {
			return checked, nil
		}
		if latest.Path != resolved.Path || currentTarget != target {
			return operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "edit target changed before commit"}, nil
		}
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}}
	if binding != nil {
		guard.admission = binding.admission
	}
	var result *EditResult
	if prepared != nil {
		result = &prepared.file.result
	}
	committed, err := s.manager.editFile(ctx, lease, resolved.Path, req.Encoding, req.ExpectedRevision, req.Create, req.ContextLines, req.Edits, req.Options, s.CheckWrite, guard, result)
	return committed, resolved, err
}
