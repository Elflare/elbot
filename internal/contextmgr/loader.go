package contextmgr

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/storage"
)

type LoadedContext struct {
	Summary  *storage.ContextSummary
	Messages []storage.Message
}

type Loader struct {
	Store storage.Store
}

const maxForkDepth = 32

func (l Loader) Load(ctx context.Context, sessionID string) (*LoadedContext, error) {
	if l.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	return l.load(ctx, sessionID, "", 0)
}

func (l Loader) LoadRawMessages(ctx context.Context, sessionID string) ([]storage.Message, error) {
	if l.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	return l.loadRawMessages(ctx, sessionID, "", 0)
}

func (l Loader) load(ctx context.Context, sessionID, upToMessageID string, depth int) (*LoadedContext, error) {
	if depth > maxForkDepth {
		return nil, fmt.Errorf("fork depth exceeds %d", maxForkDepth)
	}
	session, err := l.Store.Sessions().Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	loaded, err := l.loadOwnSummary(ctx, session.ID, upToMessageID)
	if err != nil {
		return nil, err
	}
	if loaded != nil {
		return loaded, nil
	}

	out := &LoadedContext{}
	if session.ParentSessionID != "" && session.ForkFromMessageID != "" {
		parent, err := l.load(ctx, session.ParentSessionID, session.ForkFromMessageID, depth+1)
		if err != nil {
			return nil, err
		}
		out.Summary = parent.Summary
		out.Messages = append(out.Messages, parent.Messages...)
	}

	messages, err := l.listOwnMessages(ctx, session.ID, upToMessageID)
	if err != nil {
		return nil, err
	}
	out.Messages = append(out.Messages, messages...)
	return out, nil
}

func (l Loader) loadOwnSummary(ctx context.Context, sessionID, upToMessageID string) (*LoadedContext, error) {
	var (
		summary *storage.ContextSummary
		err     error
	)
	if upToMessageID == "" {
		summary, err = l.Store.ContextSummaries().LatestBySession(ctx, sessionID)
	} else {
		summary, err = l.Store.ContextSummaries().LatestBySessionUpTo(ctx, sessionID, upToMessageID)
	}
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	messages, err := l.listOwnMessagesAfter(ctx, sessionID, summary.ToMessageID, upToMessageID)
	if err != nil {
		return nil, err
	}
	return &LoadedContext{Summary: summary, Messages: messages}, nil
}

func (l Loader) listOwnMessages(ctx context.Context, sessionID, upToMessageID string) ([]storage.Message, error) {
	if upToMessageID == "" {
		return l.Store.Messages().ListBySession(ctx, sessionID)
	}
	return l.Store.Messages().ListBySessionUpTo(ctx, sessionID, upToMessageID)
}

func (l Loader) listOwnMessagesAfter(ctx context.Context, sessionID, afterMessageID, upToMessageID string) ([]storage.Message, error) {
	var (
		messages []storage.Message
		err      error
	)
	if upToMessageID == "" {
		messages, err = l.Store.Messages().ListBySessionAfter(ctx, sessionID, afterMessageID)
	} else {
		messages, err = l.Store.Messages().ListBySessionAfterUpTo(ctx, sessionID, afterMessageID, upToMessageID)
	}
	if errors.Is(err, storage.ErrNotFound) {
		// A fork summary may cover an ancestor range, leaving only this branch's messages.
		return l.listOwnMessages(ctx, sessionID, upToMessageID)
	}
	return messages, err
}

func (l Loader) loadRawMessages(ctx context.Context, sessionID, upToMessageID string, depth int) ([]storage.Message, error) {
	if depth > maxForkDepth {
		return nil, fmt.Errorf("fork depth exceeds %d", maxForkDepth)
	}
	session, err := l.Store.Sessions().Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	var out []storage.Message
	if session.ParentSessionID != "" && session.ForkFromMessageID != "" {
		parent, err := l.loadRawMessages(ctx, session.ParentSessionID, session.ForkFromMessageID, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, parent...)
	}
	own, err := l.listOwnMessages(ctx, session.ID, upToMessageID)
	if err != nil {
		return nil, err
	}
	return append(out, own...), nil
}
