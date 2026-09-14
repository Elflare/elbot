package media

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"sync"
	"time"

	"elbot/internal/storage"
)

// RetainSessionToolArguments keeps exact media ID values from an executed tool call for its Session.
func (m *Manager) RetainSessionToolArguments(ctx context.Context, sessionID, arguments string) error {
	if sessionID == "" {
		return errors.New("session ID is required for tool media references")
	}
	ids := argumentMediaIDs(arguments)
	if len(ids) == 0 {
		return nil
	}
	createdAt := m.Now()
	references := make([]storage.MediaReference, 0, len(ids))
	for _, id := range ids {
		references = append(references, storage.MediaReference{
			MediaID: id, OwnerType: "session_tool", OwnerID: sessionID,
			Purpose: "input", SessionID: sessionID, CreatedAt: createdAt,
		})
	}
	return m.Store.MediaReferences().AddAll(ctx, references)
}

func argumentMediaIDs(arguments string) []string {
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
	ref := storage.MediaReference{MediaID: id, OwnerType: "request", OwnerID: storage.NewID(), Purpose: "temporary"}
	if err := m.AddReference(ctx, &ref); err != nil {
		return nil, err
	}
	return func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return m.RemoveReference(cleanupCtx, ref)
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
