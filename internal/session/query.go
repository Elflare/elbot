package session

import (
	"context"
	"errors"

	"elbot/internal/storage"
)

func (s *Service) List(ctx context.Context, scope Scope, query string, limit int) ([]storage.SessionSummary, error) {
	return s.ListPage(ctx, scope, query, limit, 0, false)
}

func (s *Service) ListPage(ctx context.Context, scope Scope, query string, limit, offset int, archivedOnly bool) ([]storage.SessionSummary, error) {
	return s.store.Sessions().List(ctx, storage.ListSessionsRequest{
		ActorID:                 scope.ActorID,
		Platform:                scope.Platform,
		PlatformScopeID:         scope.PlatformScopeID,
		IncludeAllPlatforms:     scope.IsCLI,
		IncludeSamePlatformCron: !scope.IsCLI,
		ArchivedOnly:            archivedOnly,
		Query:                   query,
		Limit:                   limit,
		Offset:                  offset,
	})
}

func (s *Service) ListResumablePage(ctx context.Context, scope Scope, limit, offset int) ([]storage.SessionSummary, error) {
	currentID := ""
	current, err := s.Current(ctx, scope)
	if err == nil {
		currentID = current.ID
	} else if !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}
	return s.store.Sessions().List(ctx, storage.ListSessionsRequest{
		ActorID:                 scope.ActorID,
		Platform:                scope.Platform,
		PlatformScopeID:         scope.PlatformScopeID,
		IncludeAllPlatforms:     scope.IsCLI,
		IncludeSamePlatformCron: !scope.IsCLI,
		ExcludeSessionID:        currentID,
		OrderByUpdatedAt:        true,
		Limit:                   limit,
		Offset:                  offset,
	})
}

func (s *Service) Status(ctx context.Context, scope Scope) (*Status, error) {
	session, err := s.Current(ctx, scope)
	if err != nil {
		return nil, err
	}
	messages, err := s.store.Messages().ListBySession(ctx, session.ID)
	if err != nil {
		return nil, err
	}
	status := &Status{Session: session, MessageCount: len(messages)}
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		switch message.Role {
		case storage.RoleUser:
			if status.LastUserPreview == "" {
				status.LastUserPreview = preview(message.Content)
			}
		case storage.RoleAssistant:
			if status.LastAnswerPreview == "" {
				status.LastAnswerPreview = preview(message.Content)
			}
		}
		if status.LastUserPreview != "" && status.LastAnswerPreview != "" {
			break
		}
	}
	return status, nil
}
