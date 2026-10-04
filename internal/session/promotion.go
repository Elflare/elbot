package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"elbot/internal/chatinfo"
	"elbot/internal/storage"
)

var ErrForegroundSession = errors.New("会话已由前台接管")

type ForegroundOrigin struct {
	Kind     string `json:"kind"`
	OwnerID  string `json:"owner_id"`
	Platform string `json:"platform"`
	ScopeID  string `json:"scope_id"`
}

func WasPromoted(row *storage.Session) bool {
	if row == nil {
		return false
	}
	fields, err := storage.DecodeSessionMetadata(row.Metadata)
	return err == nil && len(fields["foreground_origin"]) > 0
}

func IsBackground(row *storage.Session) bool {
	if row == nil || WasPromoted(row) {
		return false
	}
	fields, _ := storage.DecodeSessionMetadata(row.Metadata)
	var kind string
	_ = json.Unmarshal(fields["background_kind"], &kind)
	return strings.TrimSpace(kind) != "" || strings.HasPrefix(row.PlatformScopeID, "cron:") || strings.HasPrefix(row.PlatformScopeID, "elnis:")
}

func (scope Scope) acceptsBackground() bool {
	return scope.IsCLI || scope.ConversationKind == chatinfo.ConversationPrivate
}

// SetForegroundActivation installs the execution participant before startup.
// It runs synchronously under admission after commit and must not do I/O or
// acquire session/scope admission. Lifecycle consumers use BindingChanged.
func (s *Service) SetForegroundActivation(adopt func(context.Context, *storage.Session, *Binding)) {
	s.foregroundActivation = adopt
}

func (s *Service) activateExisting(ctx context.Context, scope Scope, id string, unarchive bool) (*storage.Session, error) {
	ctx, release, err := s.EnterActivation(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.canReplaceCurrent(scope, id); err != nil {
		return nil, err
	}
	promoted := false
	row, err := s.store.Sessions().Mutate(ctx, id, func(row *storage.Session) error {
		if !s.canAccess(scope, row) {
			return errors.New("session is not in current platform scope")
		}
		if IsBackground(row) {
			fields, err := storage.DecodeSessionMetadata(row.Metadata)
			if err != nil {
				return err
			}
			var kind string
			_ = json.Unmarshal(fields["background_kind"], &kind)
			if err := fields.Set("foreground_origin", ForegroundOrigin{Kind: kind, OwnerID: row.OwnerID, Platform: row.Platform, ScopeID: row.PlatformScopeID}); err != nil {
				return err
			}
			delete(fields, "background_kind")
			row.Metadata, err = fields.Encode()
			if err != nil {
				return err
			}
			row.OwnerID, row.Platform, row.PlatformScopeID = scope.ActorID, scope.Platform, scope.PlatformScopeID
			promoted = true
		}
		if unarchive {
			row.ArchivedAt = nil
		}
		row.UpdatedAt = storage.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.setCurrent(ctx, scope, row.ID, ChangeResume)
	if promoted && s.foregroundActivation != nil {
		s.mu.Lock()
		binding := s.current[scope.Key()]
		s.mu.Unlock()
		s.foregroundActivation(ctx, row, binding)
	}
	return row, nil
}
