package session

import (
	"context"

	"elbot/internal/storage"
)

// BackgroundCopyRequest copies task history without activating a foreground
// binding or inheriting execution/tool state. Metadata contains business fields;
// naming and background identity are always supplied by Session.
type BackgroundCopyRequest struct {
	SourceSessionID string
	Kind            string
	Name            string
	Title           string
	Metadata        storage.SessionMetadata
}

func (s *Service) CopyBackground(ctx context.Context, target Scope, req BackgroundCopyRequest) (*storage.Session, error) {
	ctx, release, err := s.EnterSessions(ctx, req.SourceSessionID)
	if err != nil {
		return nil, err
	}
	defer release()
	source, err := s.store.Sessions().Get(ctx, req.SourceSessionID)
	if err != nil {
		return nil, err
	}
	if WasPromoted(source) || !IsBackground(source) {
		return nil, ErrForegroundSession
	}
	messages, err := s.store.Messages().ListBySession(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	seed, err := req.Metadata.Encode()
	if err != nil {
		return nil, err
	}
	seed, err = InheritOrigin(source, seed)
	if err != nil {
		return nil, err
	}
	background := BackgroundRequest{Kind: req.Kind, Name: req.Name, Title: req.Title}
	metadata, err := backgroundMetadata(seed, background)
	if err != nil {
		return nil, err
	}
	row, err := s.createBackground(ctx, target, background, metadata)
	if err != nil {
		return nil, err
	}
	for _, msg := range messages {
		if err := ctx.Err(); err != nil {
			return row, err
		}
		msg.ID, msg.SessionID = "", row.ID
		msg.ParentMessageID, msg.ReplyToMessageID, msg.ReplyToPlatformMessageID = "", "", ""
		if err := s.store.Messages().Append(ctx, &msg); err != nil {
			return row, err
		}
	}
	return row, nil
}
