package fileops

import (
	"context"
	"encoding/json"
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

type editPreflight struct {
	arguments string
	path      string
	target    string
	result    EditResult
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

func (s *Service) editCall(ctx context.Context) (*fileRollbackCall, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.Manager == nil {
		return nil, fmt.Errorf("file service is not configured")
	}
	call, _ := ctx.Value(fileRollbackContextKey{}).(*fileRollbackCall)
	if call != nil && call.service != s {
		return nil, ErrRollbackExpired
	}
	lease, err := s.EditSession(ctx)
	if err != nil {
		return nil, err
	}
	if lease != nil {
		if err := lease.check(ctx); err != nil {
			return nil, err
		}
	}
	return call, nil
}

func editArguments(req EditRequest) string {
	raw, _ := json.Marshal(req)
	return string(raw)
}

func checkEditPreflight(preview *editPreflight, req EditRequest, path, target string, created bool, revision string) error {
	if preview == nil {
		return nil
	}
	if preview.arguments != editArguments(req) {
		return fmt.Errorf("edit arguments changed after preflight")
	}
	if preview.path != path || preview.target != target {
		return fmt.Errorf("edit target changed after preflight")
	}
	if preview.result.Created != created || preview.result.RevisionBefore != revision {
		return fmt.Errorf("file changed after preflight")
	}
	return nil
}

// PreviewEdit pins the state that produced the confirmation diff. Repeated
// preparation in the same call cannot silently replace it with a newer state.
func (s *Service) PreviewEdit(ctx context.Context, req EditRequest) (EditResult, error) {
	call, err := s.editCall(ctx)
	if err != nil {
		return EditResult{}, err
	}
	if call != nil {
		call.mu.Lock()
		defer call.mu.Unlock()
	}
	resolved, target, err := s.editPath(ctx, req)
	if err != nil {
		return EditResult{}, err
	}
	unlock, err := s.Manager.lockTarget(ctx, target)
	if err != nil {
		return EditResult{}, err
	}
	defer unlock()
	result, err := EditFileWithOptions(resolved.Path, req.Encoding, req.ExpectedRevision, req.Create, true, req.ContextLines, req.Edits, req.Options)
	if err != nil {
		return EditResult{}, fmt.Errorf("preflight edit_file: %w", err)
	}
	if err := checkFileTarget(resolved.Path, target, result.Created); err != nil {
		return EditResult{}, err
	}
	if call != nil {
		if err := checkEditPreflight(call.edit, req, resolved.Path, target, result.Created, result.RevisionBefore); err != nil {
			return EditResult{}, err
		}
		if call.edit == nil {
			call.edit = &editPreflight{arguments: editArguments(req), path: resolved.Path, target: target, result: result}
		}
		return call.edit.result, nil
	}
	return result, nil
}

// Edit uses the same target locks for recorded foreground and unrecorded
// background operations. Temporary output is prepared before commit admission.
func (s *Service) Edit(ctx context.Context, req EditRequest) (EditResult, workspace.ResolvedPath, error) {
	call, err := s.editCall(ctx)
	if err != nil {
		return EditResult{}, workspace.ResolvedPath{}, err
	}
	if call != nil {
		call.mu.Lock()
		defer call.mu.Unlock()
	}
	resolved, target, err := s.editPath(ctx, req)
	if err != nil {
		return EditResult{}, resolved, err
	}
	lease, err := s.EditSession(ctx)
	if err != nil {
		return EditResult{}, resolved, err
	}
	ctx = withCommitValidation(ctx, func(locked context.Context) error {
		latest, currentTarget, err := s.editPath(locked, req)
		if err != nil {
			return err
		}
		if latest.Path != resolved.Path || currentTarget != target {
			return fmt.Errorf("edit target changed before commit")
		}
		return nil
	})
	options := req.Options
	options.beforeWrite = func(file File, created bool, _ os.FileMode) error {
		if call != nil {
			return checkEditPreflight(call.edit, req, resolved.Path, target, created, ContentRevision(file.Bytes))
		}
		return nil
	}
	result, err := s.Manager.editFile(ctx, lease, resolved.Path, req.Encoding, req.ExpectedRevision, req.Create, req.ContextLines, req.Edits, options, s.CheckWrite)
	return result, resolved, err
}
