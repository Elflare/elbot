package session

import (
	"context"
	"encoding/json"
	"fmt"

	"elbot/internal/llm"
	"elbot/internal/storage"
)

const originMetadataKey = "llm_origin"

// Origin reads only the session's saved facts, never current provider config.
func Origin(row *storage.Session) (llm.Origin, bool, error) {
	if row == nil {
		return llm.Origin{}, false, nil
	}
	fields, err := storage.DecodeSessionMetadata(row.Metadata)
	if err != nil {
		return llm.Origin{}, false, err
	}
	raw, ok := fields[originMetadataKey]
	if !ok {
		return llm.Origin{}, false, nil
	}
	var origin llm.Origin
	if err := json.Unmarshal(raw, &origin); err != nil {
		return llm.Origin{}, false, fmt.Errorf("decode session origin: %w", err)
	}
	if origin.APIType == "" {
		return llm.Origin{}, false, fmt.Errorf("session origin requires a protocol")
	}
	return origin, true, nil
}

// RegisterOrigin uses the original binding or the existing session gate. It never
// replaces a known historical origin with the current model's configuration.
func (s *Service) RegisterOrigin(ctx context.Context, id string, target llm.Origin) (*storage.Session, error) {
	if target.APIType == "" || target.Provider == "" {
		return nil, fmt.Errorf("dialogue origin requires a protocol and provider")
	}
	var release func()
	var err error
	if binding, ok := BindingFromContext(ctx); ok {
		if binding.SessionID() != id {
			return nil, fmt.Errorf("dialogue origin session binding mismatch")
		}
		ctx, release, err = s.EnterBinding(ctx, binding)
	} else {
		ctx, release, err = s.EnterSessions(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	defer release()
	return s.store.Sessions().Mutate(ctx, id, func(row *storage.Session) error {
		current, known, err := Origin(row)
		if err != nil {
			return err
		}
		if known {
			if current.APIType != target.APIType {
				return fmt.Errorf("source protocol %q cannot be reinterpreted as %q", current.APIType, target.APIType)
			}
			if current.Provider != "" {
				return nil
			}
		}
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		if err := fields.Set(originMetadataKey, target); err != nil {
			return err
		}
		row.Metadata, err = fields.Encode()
		return err
	})
}

// InheritOrigin transfers only material ownership, not source runtime state.
func InheritOrigin(source *storage.Session, metadata string) (string, error) {
	origin, known, err := Origin(source)
	if err != nil {
		return "", err
	}
	fields, err := storage.DecodeSessionMetadata(metadata)
	if err != nil {
		return "", err
	}
	if known {
		if err := fields.Set(originMetadataKey, origin); err != nil {
			return "", err
		}
	} else {
		delete(fields, originMetadataKey)
	}
	return fields.Encode()
}
