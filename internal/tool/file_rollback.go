package tool

import (
	"context"
	"fmt"
	"sync"

	"elbot/internal/security"
	"elbot/internal/utils/fileops"
)

// FileRollbackService shares path policy and memory backups between tools and
// slash commands. Session lifecycle is supplied by Agent, not by model arguments.
type FileRollbackService struct {
	Manager    *fileops.RollbackManager
	CheckWrite func(string) error
}

func NewFileRollbackService(checkWrite func(string) error) *FileRollbackService {
	return &FileRollbackService{Manager: fileops.NewRollbackManager(), CheckWrite: checkWrite}
}

type fileRollbackContextKey struct{}

type fileRollbackCall struct {
	service   *FileRollbackService
	scopeKey  string
	sessionID string
	session   *fileops.RollbackSession
	mu        sync.Mutex
	path      string
	id        uint64
}

// WithSession preserves the original lease and preview when a tool request
// context is derived after confirmation. A switched-away lease cannot revive.
func (s *FileRollbackService) WithSession(ctx context.Context, scopeKey, sessionID string) context.Context {
	if s == nil || s.Manager == nil {
		return ctx
	}
	if previous, ok := ctx.Value(fileRollbackContextKey{}).(*fileRollbackCall); ok &&
		previous.service == s && previous.scopeKey == scopeKey && previous.sessionID == sessionID {
		return ctx
	}
	session, _ := s.Manager.Session(scopeKey, sessionID)
	return context.WithValue(ctx, fileRollbackContextKey{}, &fileRollbackCall{
		service: s, scopeKey: scopeKey, sessionID: sessionID, session: session,
	})
}

// EditSession returns nil when recording is not enabled for this call (for
// example a background edit), but an invalid foreground lease is an error.
func (s *FileRollbackService) EditSession(ctx context.Context) (*fileops.RollbackSession, error) {
	if BackgroundContext(ctx) {
		return nil, nil
	}
	call, ok := ctx.Value(fileRollbackContextKey{}).(*fileRollbackCall)
	if !ok {
		return nil, nil
	}
	if call.service != s || call.session == nil {
		return nil, fileops.ErrRollbackExpired
	}
	return call.session, nil
}

func (s *FileRollbackService) current(ctx context.Context) (*fileRollbackCall, error) {
	if BackgroundContext(ctx) {
		return nil, fmt.Errorf("rollback is only available in foreground sessions")
	}
	actor, ok := security.ActorFromContext(ctx)
	if !ok || actor.Role != security.RoleSuperadmin {
		return nil, fmt.Errorf("rollback requires superadmin role")
	}
	if s == nil || s.Manager == nil {
		return nil, fmt.Errorf("file rollback is not configured")
	}
	call, ok := ctx.Value(fileRollbackContextKey{}).(*fileRollbackCall)
	if !ok || call.service != s || call.session == nil {
		return nil, fileops.ErrRollbackExpired
	}
	return call, nil
}

func (s *FileRollbackService) List(ctx context.Context) ([]fileops.RollbackInfo, error) {
	call, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return call.session.List()
}

func (s *FileRollbackService) Preview(ctx context.Context, rawPath string) (fileops.RollbackPreview, error) {
	call, err := s.current(ctx)
	if err != nil {
		return fileops.RollbackPreview{}, err
	}
	resolved, err := ResolveWorkspacePath(ctx, rawPath, PathResolveOptions{})
	if err != nil {
		return fileops.RollbackPreview{}, err
	}
	call.mu.Lock()
	defer call.mu.Unlock()
	if call.path != "" && call.path != resolved.Path {
		return fileops.RollbackPreview{}, fmt.Errorf("rollback path changed after preflight")
	}
	preview, err := call.session.Preview(ctx, resolved.Path, call.id, s.CheckWrite)
	if err != nil {
		return fileops.RollbackPreview{}, err
	}
	call.path, call.id = resolved.Path, preview.ID
	return preview, nil
}

// expectedID is only used by human-facing commands. Model calls use the record
// pinned by preflight, or the path's latest record when called directly.
func (s *FileRollbackService) Rollback(ctx context.Context, rawPath string, expectedID uint64) (fileops.RollbackResult, error) {
	call, err := s.current(ctx)
	if err != nil {
		return fileops.RollbackResult{}, err
	}
	resolved, err := ResolveWorkspacePath(ctx, rawPath, PathResolveOptions{})
	if err != nil {
		return fileops.RollbackResult{}, err
	}
	call.mu.Lock()
	defer call.mu.Unlock()
	if call.path != "" && call.path != resolved.Path {
		return fileops.RollbackResult{}, fmt.Errorf("rollback path changed after preflight")
	}
	if call.id != 0 {
		if expectedID != 0 && call.id != expectedID {
			return fileops.RollbackResult{}, fileops.ErrRollbackNotFound
		}
		expectedID = call.id
	}
	return call.session.Rollback(ctx, resolved.Path, expectedID, s.CheckWrite)
}
