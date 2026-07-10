package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const (
	maxSharedValueBytes = 1 << 20
	maxSharedStateBytes = 32 << 20
)

// SharedState is an in-memory, JSON-only coordination store shared by hooks.
// It intentionally survives hook restarts, but not an ElBot process restart.
type SharedState struct {
	mu    sync.RWMutex
	items map[string]json.RawMessage
	size  int
}

func NewSharedState() *SharedState {
	return &SharedState{items: map[string]json.RawMessage{}}
}

func (s *SharedState) Get(key string) (json.RawMessage, bool) {
	if s == nil {
		return nil, false
	}
	key = strings.TrimSpace(key)
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.items[key]
	return append(json.RawMessage(nil), value...), ok
}

func (s *SharedState) Set(key string, value json.RawMessage) error {
	if s == nil {
		return fmt.Errorf("hook shared state is not configured")
	}
	key, value, err := normalizeSharedValue(key, value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.items[key]
	nextSize := s.size - len(previous) + len(value)
	if nextSize > maxSharedStateBytes {
		return fmt.Errorf("hook shared state exceeds %d MiB limit", maxSharedStateBytes>>20)
	}
	s.items[key] = value
	s.size = nextSize
	return nil
}

func (s *SharedState) Delete(key string) bool {
	if s == nil {
		return false
	}
	key = strings.TrimSpace(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.items[key]
	if !ok {
		return false
	}
	delete(s.items, key)
	s.size -= len(value)
	return true
}

func (s *SharedState) List(prefix string) []string {
	if s == nil {
		return nil
	}
	prefix = strings.TrimSpace(prefix)
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.items))
	for key := range s.items {
		if prefix == "" || strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// CompareAndSwap atomically replaces a value only when expected matches the
// current JSON value. A nil expected value means the key must not exist.
func (s *SharedState) CompareAndSwap(key string, expected, value json.RawMessage) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("hook shared state is not configured")
	}
	key, value, err := normalizeSharedValue(key, value)
	if err != nil {
		return false, err
	}
	if len(expected) > 0 && !json.Valid(expected) {
		return false, fmt.Errorf("shared expected value must be valid JSON")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.items[key]
	if len(expected) == 0 {
		if exists {
			return false, nil
		}
	} else if !exists || !bytes.Equal(compactJSON(current), compactJSON(expected)) {
		return false, nil
	}
	nextSize := s.size - len(current) + len(value)
	if nextSize > maxSharedStateBytes {
		return false, fmt.Errorf("hook shared state exceeds %d MiB limit", maxSharedStateBytes>>20)
	}
	s.items[key] = value
	s.size = nextSize
	return true, nil
}

func normalizeSharedValue(key string, value json.RawMessage) (string, json.RawMessage, error) {
	key = strings.TrimSpace(key)
	if !strings.Contains(key, "/") || strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") {
		return "", nil, fmt.Errorf("shared key must use namespace/key form")
	}
	if len(value) == 0 || !json.Valid(value) {
		return "", nil, fmt.Errorf("shared value must be valid JSON")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return "", nil, fmt.Errorf("compact shared value: %w", err)
	}
	if compact.Len() > maxSharedValueBytes {
		return "", nil, fmt.Errorf("shared value exceeds %d MiB limit", maxSharedValueBytes>>20)
	}
	return key, append(json.RawMessage(nil), compact.Bytes()...), nil
}

func compactJSON(value json.RawMessage) []byte {
	var compact bytes.Buffer
	if json.Compact(&compact, value) != nil {
		return value
	}
	return compact.Bytes()
}
