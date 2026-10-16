package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"elbot/internal/events"
	"elbot/internal/storage"
)

// RetainSessionToolArguments keeps exact media ID values from an executed tool call for its Session.
// Its log result describes the reference transaction independently of the original
// business error; arguments without media references do not invent a save.
func (m *Manager) RetainSessionToolArguments(ctx context.Context, sessionID, arguments string) (events.LogResult, error) {
	if sessionID == "" {
		err := errors.New("session ID is required for tool media references")
		return events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}, err
	}
	ids := ArgumentMediaIDs(arguments)
	if len(ids) == 0 {
		return events.LogResult{ResultStatus: events.ResultSkipped, Content: "tool arguments contain no media references"}, nil
	}
	createdAt := m.Now()
	references := make([]storage.MediaReference, 0, len(ids))
	for _, id := range ids {
		references = append(references, storage.MediaReference{
			MediaID: id, OwnerType: "session_tool", OwnerID: sessionID,
			Purpose: "input", SessionID: sessionID, CreatedAt: createdAt,
		})
	}
	if err := m.Store.MediaReferences().AddAll(ctx, references); err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("retain tool media references: count=%d error=%v", len(references), err)}
		// AddAll returns one transaction failure, before this source wraps it.
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = events.ResultCanceled
		}
		return logResult, err
	}
	return events.LogResult{ResultStatus: events.ResultSucceeded, Content: fmt.Sprintf("retained tool media references: count=%d", len(references))}, nil
}

func ArgumentMediaIDs(arguments string) []string {
	var value any
	if err := json.Unmarshal([]byte(arguments), &value); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case string:
			if ValidID(value) {
				seen[value] = true
			}
		case []any:
			for _, item := range value {
				visit(item)
			}
		case map[string]any:
			for _, item := range value {
				visit(item)
			}
		}
	}
	visit(value)
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Hold keeps an in-flight consumer alive independently of persistent owners.
func (m *Manager) Hold(ctx context.Context, id string) (func() error, error) {
	metadata, err := m.Metadata(ctx, id)
	if err != nil {
		return nil, err
	}
	return m.holdMedia(ctx, metadata)
}

func (m *Manager) holdMedia(ctx context.Context, metadata *storage.Media) (func() error, error) {
	ref := storage.MediaReference{MediaID: metadata.ID, OwnerType: "request", OwnerID: storage.NewID(), Purpose: "temporary"}
	if err := m.Store.MediaReferences().Add(ctx, &ref); err != nil {
		return nil, err
	}
	return func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return m.Store.MediaReferences().Remove(cleanupCtx, ref)
	}, nil
}

type referencedReader struct {
	io.ReadCloser
	release func() error
	once    sync.Once
	err     error
}

func (r *referencedReader) Close() error {
	r.once.Do(func() { r.err = errors.Join(r.ReadCloser.Close(), r.release()) })
	return r.err
}
