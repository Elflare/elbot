// Package session manages conversation sessions and their lifecycle.
package session

import (
	"context"
	"errors"
	"strings"
	"time"

	"elbot/internal/storage"
)

type IdleExpirationConfig struct {
	GroupUserTTLMinutes         int
	GroupSuperadminTTLMinutes   int
	PrivateUserTTLMinutes       int
	PrivateSuperadminTTLMinutes int
}

type ExpireIdleRequest struct {
	Scope        Scope
	IsSuperadmin bool
	Config       IdleExpirationConfig
	Now          time.Time
}

type ExpireIdleResult struct {
	Expired    bool
	SessionID  string
	TTLMinutes int
}

func (s *Service) ExpireIdleCurrent(ctx context.Context, req ExpireIdleRequest) (ExpireIdleResult, error) {
	ttlMinutes := req.Config.ttlMinutes(req.Scope, req.IsSuperadmin)
	if ttlMinutes <= 0 {
		return ExpireIdleResult{}, nil
	}
	session, err := s.Current(ctx, req.Scope)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ExpireIdleResult{}, nil
		}
		return ExpireIdleResult{}, err
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	if !session.UpdatedAt.Before(now.Add(-time.Duration(ttlMinutes) * time.Minute)) {
		return ExpireIdleResult{}, nil
	}
	session.UpdatedAt = now
	if err := s.store.Sessions().Update(ctx, session); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ExpireIdleResult{}, nil
		}
		return ExpireIdleResult{}, err
	}
	s.clearCurrentIf(req.Scope, session.ID)
	return ExpireIdleResult{Expired: true, SessionID: session.ID, TTLMinutes: ttlMinutes}, nil
}

func (c IdleExpirationConfig) ttlMinutes(scope Scope, isSuperadmin bool) int {
	if isGroupScope(scope.PlatformScopeID) {
		if isSuperadmin {
			return c.GroupSuperadminTTLMinutes
		}
		return c.GroupUserTTLMinutes
	}
	if isSuperadmin {
		return c.PrivateSuperadminTTLMinutes
	}
	return c.PrivateUserTTLMinutes
}

func isGroupScope(scopeID string) bool {
	scopeID = strings.TrimSpace(scopeID)
	return strings.HasPrefix(scopeID, "group:") || strings.HasPrefix(scopeID, "supergroup:")
}
