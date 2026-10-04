package storage

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SessionMetadata preserves unknown fields without converting JSON numbers.
type SessionMetadata map[string]json.RawMessage

func DecodeSessionMetadata(raw string) (SessionMetadata, error) {
	fields := SessionMetadata{}
	if strings.TrimSpace(raw) == "" {
		return fields, nil
	}
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, fmt.Errorf("decode session metadata: %w", err)
	}
	if fields == nil {
		return nil, fmt.Errorf("session metadata must be an object")
	}
	return fields, nil
}

func (m SessionMetadata) Set(key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode session metadata %s: %w", key, err)
	}
	m[key] = raw
	return nil
}

func (m SessionMetadata) Encode() (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("encode session metadata: %w", err)
	}
	return string(raw), nil
}
