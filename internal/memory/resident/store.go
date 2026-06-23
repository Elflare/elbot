package resident

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
)

const (
	DefaultCoreMaxUnits   = 200
	DefaultNormalMaxUnits = 300
)

var ErrNotFound = errors.New("resident memory not found")

type Limits struct {
	Core   int
	Normal int
}

type Memory struct {
	Core   string `json:"core"`
	Normal string `json:"normal"`
}

func (m Memory) Empty() bool {
	return strings.TrimSpace(m.Core) == "" && strings.TrimSpace(m.Normal) == ""
}

func (m Memory) Text() string {
	parts := []string{}
	for _, part := range []string{m.Core, m.Normal} {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

type ResidentMemory struct {
	Platform  string `toml:"platform"`
	ActorID   string `toml:"actor_id"`
	Core      string `toml:"core"`
	Normal    string `toml:"normal"`
	CreatedAt string `toml:"created_at"`
	UpdatedAt string `toml:"updated_at"`
}

type tomlFile struct {
	ResidentMemories []ResidentMemory `toml:"resident_memories"`
}

type Store struct {
	Path   string
	Limits Limits
	mu     sync.Mutex
	cache  storeCache
}

type storeCache struct {
	loaded bool
	file   tomlFile
	state  fileState
}

type fileState struct {
	exists  bool
	size    int64
	modTime time.Time
}

func NewStore(path string) *Store {
	return NewStoreWithLimits(path, Limits{})
}

func NewStoreWithLimits(path string, limits Limits) *Store {
	return &Store{Path: path, Limits: normalizeLimits(limits)}
}

func ActorScope(actor security.Actor) session.Scope {
	return session.Scope{ActorID: actor.ID, Platform: actor.Platform}
}

func (s *Store) Read(ctx context.Context, scope session.Scope) (Memory, error) {
	if err := ctx.Err(); err != nil {
		return Memory{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadLocked()
	if err != nil {
		return Memory{}, err
	}
	idx := findMemory(file.ResidentMemories, scope)
	if idx < 0 {
		return Memory{}, ErrNotFound
	}
	memory := file.ResidentMemories[idx]
	return Memory{Core: strings.TrimSpace(memory.Core), Normal: strings.TrimSpace(memory.Normal)}, nil
}

func (s *Store) WriteCore(ctx context.Context, scope session.Scope, content string) error {
	return s.update(ctx, scope, func(memory *ResidentMemory) {
		memory.Core = strings.TrimSpace(content)
	})
}

func (s *Store) WriteNormal(ctx context.Context, scope session.Scope, content string) error {
	return s.update(ctx, scope, func(memory *ResidentMemory) {
		memory.Normal = strings.TrimSpace(content)
	})
}

func (s *Store) AppendNormal(ctx context.Context, scope session.Scope, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("resident memory normal content is required")
	}
	return s.update(ctx, scope, func(memory *ResidentMemory) {
		existing := strings.TrimSpace(memory.Normal)
		if existing == "" {
			memory.Normal = content
		} else {
			memory.Normal = existing + " " + content
		}
	})
}

func (s *Store) DeleteNormal(ctx context.Context, scope session.Scope) error {
	return s.WriteNormal(ctx, scope, "")
}

func (s *Store) LimitsOrDefault() Limits {
	return normalizeLimits(s.Limits)
}

func (s *Store) update(ctx context.Context, scope session.Scope, update func(*ResidentMemory)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateScope(scope); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadLocked()
	if err != nil {
		return err
	}
	now := storage.FormatTime(storage.Now())
	idx := findMemory(file.ResidentMemories, scope)
	if idx < 0 {
		file.ResidentMemories = append(file.ResidentMemories, ResidentMemory{Platform: scope.Platform, ActorID: scope.ActorID, CreatedAt: now})
		idx = len(file.ResidentMemories) - 1
	}
	memory := file.ResidentMemories[idx]
	update(&memory)
	memory.Core = strings.TrimSpace(memory.Core)
	memory.Normal = strings.TrimSpace(memory.Normal)
	if err := s.validateMemory(memory); err != nil {
		return err
	}
	if memory.Core == "" && memory.Normal == "" {
		file.ResidentMemories = append(file.ResidentMemories[:idx], file.ResidentMemories[idx+1:]...)
	} else {
		memory.UpdatedAt = now
		if memory.CreatedAt == "" {
			memory.CreatedAt = now
		}
		file.ResidentMemories[idx] = memory
	}
	return s.saveLocked(file)
}

func (s *Store) validateMemory(memory ResidentMemory) error {
	limits := s.LimitsOrDefault()
	if units := CountUnits(memory.Core); units > limits.Core {
		return fmt.Errorf("resident memory core is too long: %d/%d units", units, limits.Core)
	}
	if units := CountUnits(memory.Normal); units > limits.Normal {
		return fmt.Errorf("resident memory normal is too long: %d/%d units", units, limits.Normal)
	}
	return nil
}

func CountUnits(content string) int {
	units := 0
	inWord := false
	for _, r := range content {
		switch {
		case isCJK(r):
			units++
			inWord = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if !inWord {
				units++
				inWord = true
			}
		default:
			inWord = false
		}
	}
	return units
}

func isCJK(r rune) bool {
	return (r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0x3040 && r <= 0x30FF) ||
		(r >= 0xAC00 && r <= 0xD7AF)
}

func (s *Store) loadLocked() (tomlFile, error) {
	path := strings.TrimSpace(s.Path)
	if path == "" {
		return tomlFile{}, fmt.Errorf("resident memory path is required")
	}
	state, err := currentFileState(path)
	if err != nil {
		return tomlFile{}, err
	}
	if s.cache.loaded && sameFileState(s.cache.state, state) {
		return cloneFile(s.cache.file), nil
	}
	if !state.exists {
		file := tomlFile{}
		s.cache = storeCache{loaded: true, file: file, state: state}
		return file, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return tomlFile{}, fmt.Errorf("read resident memory %q: %w", path, err)
	}
	var file tomlFile
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := toml.Unmarshal(data, &file); err != nil {
			return tomlFile{}, fmt.Errorf("parse resident memory %q: %w", path, err)
		}
	}
	s.cache = storeCache{loaded: true, file: cloneFile(file), state: state}
	return file, nil
}

func (s *Store) saveLocked(file tomlFile) error {
	path := strings.TrimSpace(s.Path)
	if path == "" {
		return fmt.Errorf("resident memory path is required")
	}
	data, err := toml.Marshal(file)
	if err != nil {
		return fmt.Errorf("marshal resident memory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create resident memory dir %q: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write resident memory %q: %w", path, err)
	}
	state, err := currentFileState(path)
	if err != nil {
		return err
	}
	s.cache = storeCache{loaded: true, file: cloneFile(file), state: state}
	return nil
}

func currentFileState(path string) (fileState, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileState{}, nil
	}
	if err != nil {
		return fileState{}, fmt.Errorf("stat resident memory %q: %w", path, err)
	}
	return fileState{exists: true, size: info.Size(), modTime: info.ModTime()}, nil
}

func sameFileState(left, right fileState) bool {
	return left.exists == right.exists && left.size == right.size && left.modTime.Equal(right.modTime)
}

func cloneFile(file tomlFile) tomlFile {
	out := tomlFile{ResidentMemories: make([]ResidentMemory, len(file.ResidentMemories))}
	copy(out.ResidentMemories, file.ResidentMemories)
	return out
}

func findMemory(memories []ResidentMemory, scope session.Scope) int {
	platform := strings.TrimSpace(scope.Platform)
	actorID := strings.TrimSpace(scope.ActorID)
	for i, memory := range memories {
		if memory.Platform == platform && memory.ActorID == actorID {
			return i
		}
	}
	return -1
}

func validateScope(scope session.Scope) error {
	if strings.TrimSpace(scope.Platform) == "" {
		return fmt.Errorf("resident memory platform scope is required")
	}
	if strings.TrimSpace(scope.ActorID) == "" {
		return fmt.Errorf("resident memory actor scope is required")
	}
	return nil
}

func normalizeLimits(limits Limits) Limits {
	if limits.Core <= 0 {
		limits.Core = DefaultCoreMaxUnits
	}
	if limits.Normal <= 0 {
		limits.Normal = DefaultNormalMaxUnits
	}
	return limits
}
