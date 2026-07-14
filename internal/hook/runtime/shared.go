package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxSharedKeyBytes     = 256
	maxSharedValueBytes   = 1 << 20
	maxSharedStateBytes   = 32 << 20
	maxSharedStateEntries = 10_000
	defaultSharedTTL      = 10 * time.Minute
	sharedCleanupInterval = time.Minute
	maxSharedTTLSeconds   = int64((1<<63 - 1) / 1_000_000_000)
)

type sharedEntry struct {
	value      json.RawMessage
	ttl        time.Duration
	lastAccess time.Time
}

// SharedState is an in-memory, JSON-only coordination store shared by hooks.
// It intentionally survives hook restarts, but not an ElBot process restart.
type SharedState struct {
	mu         sync.RWMutex
	items      map[string]sharedEntry
	size       int
	maxEntries int
	maxSize    int
	now        func() time.Time
}

func NewSharedState() *SharedState {
	return &SharedState{
		items:      map[string]sharedEntry{},
		maxEntries: maxSharedStateEntries,
		maxSize:    maxSharedStateBytes,
		now:        time.Now,
	}
}

// HandleRequest applies a hook.v2 shared-state request.
func (s *SharedState) HandleRequest(method string, params json.RawMessage) (any, error) {
	switch method {
	case "shared.get":
		var request struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, err
		}
		value, ok := s.Get(request.Key)
		return map[string]any{"found": ok, "value": json.RawMessage(value)}, nil
	case "shared.set":
		var request struct {
			Key        string          `json:"key"`
			Value      json.RawMessage `json:"value"`
			TTLSeconds *int64          `json:"ttl_seconds"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, err
		}
		ttl, err := sharedTTL(request.TTLSeconds)
		if err != nil {
			return nil, err
		}
		if err := s.SetWithTTL(request.Key, request.Value, ttl); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "shared.delete":
		var request struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": s.Delete(request.Key)}, nil
	case "shared.list":
		var request struct {
			Prefix string `json:"prefix"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, err
		}
		return map[string]any{"keys": s.List(request.Prefix)}, nil
	case "shared.compare_and_swap":
		var request struct {
			Key        string          `json:"key"`
			Expected   json.RawMessage `json:"expected"`
			Value      json.RawMessage `json:"value"`
			TTLSeconds *int64          `json:"ttl_seconds"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, err
		}
		ttl, err := sharedTTL(request.TTLSeconds)
		if err != nil {
			return nil, err
		}
		swapped, err := s.CompareAndSwapWithTTL(request.Key, request.Expected, request.Value, ttl)
		if err != nil {
			return nil, err
		}
		return map[string]any{"swapped": swapped}, nil
	default:
		return nil, fmt.Errorf("unsupported hook shared method %q", method)
	}
}

func (s *SharedState) Get(key string) (json.RawMessage, bool) {
	if s == nil {
		return nil, false
	}
	key = strings.TrimSpace(key)
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.items[key]
	if !ok {
		return nil, false
	}
	if entry.expired(now) {
		s.deleteLocked(key, entry)
		return nil, false
	}
	entry.lastAccess = now
	s.items[key] = entry
	return append(json.RawMessage(nil), entry.value...), true
}

func (s *SharedState) Set(key string, value json.RawMessage) error {
	return s.SetWithTTL(key, value, defaultSharedTTL)
}

func (s *SharedState) SetWithTTL(key string, value json.RawMessage, ttl time.Duration) error {
	if s == nil {
		return fmt.Errorf("hook shared state is not configured")
	}
	key, value, err := normalizeSharedValue(key, value)
	if err != nil {
		return err
	}
	if ttl < 0 {
		return fmt.Errorf("shared ttl must not be negative")
	}
	now := s.currentTime()
	entry := sharedEntry{value: value, ttl: ttl, lastAccess: now}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	if err := s.makeRoomLocked(key, entry); err != nil {
		return err
	}
	if previous, ok := s.items[key]; ok {
		s.size -= sharedEntrySize(key, previous)
	}
	s.items[key] = entry
	s.size += sharedEntrySize(key, entry)
	return nil
}

func (s *SharedState) Delete(key string) bool {
	if s == nil {
		return false
	}
	key = strings.TrimSpace(key)
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.items[key]
	if !ok {
		return false
	}
	s.deleteLocked(key, entry)
	return !entry.expired(now)
}

func (s *SharedState) List(prefix string) []string {
	if s == nil {
		return nil
	}
	prefix = strings.TrimSpace(prefix)
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
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
	return s.CompareAndSwapWithTTL(key, expected, value, defaultSharedTTL)
}

func (s *SharedState) CompareAndSwapWithTTL(key string, expected, value json.RawMessage, ttl time.Duration) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("hook shared state is not configured")
	}
	key, value, err := normalizeSharedValue(key, value)
	if err != nil {
		return false, err
	}
	if ttl < 0 {
		return false, fmt.Errorf("shared ttl must not be negative")
	}
	if len(expected) > 0 {
		if !json.Valid(expected) {
			return false, fmt.Errorf("shared expected value must be valid JSON")
		}
		expected = compactJSON(expected)
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.items[key]
	if exists && current.expired(now) {
		s.deleteLocked(key, current)
		current, exists = sharedEntry{}, false
	}
	if len(expected) == 0 {
		if exists {
			return false, nil
		}
	} else if !exists || !bytes.Equal(current.value, expected) {
		return false, nil
	}
	entry := sharedEntry{value: value, ttl: ttl, lastAccess: now}
	if err := s.makeRoomLocked(key, entry); err != nil {
		return false, err
	}
	if exists {
		s.size -= sharedEntrySize(key, current)
	}
	s.items[key] = entry
	s.size += sharedEntrySize(key, entry)
	return true, nil
}

func (s *SharedState) PruneExpired() int {
	if s == nil {
		return 0
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneExpiredLocked(now)
}

func (s *SharedState) makeRoomLocked(key string, incoming sharedEntry) error {
	if sharedEntrySize(key, incoming) > s.maxSize {
		return fmt.Errorf("hook shared state entry exceeds %d byte limit", s.maxSize)
	}
	current, exists := s.items[key]
	currentSize := 0
	if exists {
		currentSize = sharedEntrySize(key, current)
	}
	fits := func() bool {
		count := len(s.items)
		if !exists {
			count++
		}
		return count <= s.maxEntries && s.size-currentSize+sharedEntrySize(key, incoming) <= s.maxSize
	}
	if fits() {
		return nil
	}
	type candidate struct {
		key        string
		lastAccess time.Time
	}
	candidates := make([]candidate, 0, len(s.items))
	for candidateKey, entry := range s.items {
		if candidateKey != key {
			candidates = append(candidates, candidate{key: candidateKey, lastAccess: entry.lastAccess})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].lastAccess.Equal(candidates[j].lastAccess) {
			return candidates[i].key < candidates[j].key
		}
		return candidates[i].lastAccess.Before(candidates[j].lastAccess)
	})
	for _, candidate := range candidates {
		entry, ok := s.items[candidate.key]
		if !ok {
			continue
		}
		s.deleteLocked(candidate.key, entry)
		if fits() {
			return nil
		}
	}
	return fmt.Errorf("hook shared state cannot make room for key %q", key)
}

func (s *SharedState) pruneExpiredLocked(now time.Time) int {
	removed := 0
	for key, entry := range s.items {
		if entry.expired(now) {
			s.deleteLocked(key, entry)
			removed++
		}
	}
	return removed
}

func (s *SharedState) deleteLocked(key string, entry sharedEntry) {
	delete(s.items, key)
	s.size -= sharedEntrySize(key, entry)
}

func (s *SharedState) currentTime() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

func (entry sharedEntry) expired(now time.Time) bool {
	return entry.ttl > 0 && !now.Before(entry.lastAccess.Add(entry.ttl))
}

func sharedEntrySize(key string, entry sharedEntry) int {
	return len(key) + len(entry.value)
}

func normalizeSharedValue(key string, value json.RawMessage) (string, json.RawMessage, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", nil, fmt.Errorf("shared key must not be empty")
	}
	if len(key) > maxSharedKeyBytes {
		return "", nil, fmt.Errorf("shared key exceeds %d byte limit", maxSharedKeyBytes)
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

func sharedTTL(ttlSeconds *int64) (time.Duration, error) {
	if ttlSeconds == nil {
		return defaultSharedTTL, nil
	}
	if *ttlSeconds < 0 {
		return 0, fmt.Errorf("ttl_seconds must not be negative")
	}
	if *ttlSeconds > maxSharedTTLSeconds {
		return 0, fmt.Errorf("ttl_seconds is too large")
	}
	return time.Duration(*ttlSeconds) * time.Second, nil
}
