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

	"github.com/pelletier/go-toml/v2"

	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
)

const DefaultMaxChars = 400

var ErrNotFound = errors.New("resident memory not found")

type ResidentMemory struct {
	Platform  string `toml:"platform"`
	ActorID   string `toml:"actor_id"`
	Content   string `toml:"content"`
	CreatedAt string `toml:"created_at"`
	UpdatedAt string `toml:"updated_at"`
}

type tomlFile struct {
	ResidentMemories []ResidentMemory `toml:"resident_memories"`
}

type Store struct {
	Path     string
	MaxChars int
	mu       sync.Mutex
	cache    storeCache
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
	return &Store{Path: path, MaxChars: DefaultMaxChars}
}

func ActorScope(actor security.Actor) session.Scope {
	return session.Scope{ActorID: actor.ID, Platform: actor.Platform}
}

func (s *Store) Read(ctx context.Context, scope session.Scope) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadLocked()
	if err != nil {
		return "", err
	}
	idx := findMemory(file.ResidentMemories, scope)
	if idx < 0 {
		return "", ErrNotFound
	}
	return file.ResidentMemories[idx].Content, nil
}

func (s *Store) Write(ctx context.Context, scope session.Scope, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("resident memory content is required")
	}
	maxChars := s.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	if len([]rune(content)) > maxChars {
		return fmt.Errorf("resident memory is too long: %d/%d chars", len([]rune(content)), maxChars)
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
		file.ResidentMemories = append(file.ResidentMemories, ResidentMemory{Platform: scope.Platform, ActorID: scope.ActorID, Content: content, CreatedAt: now, UpdatedAt: now})
	} else {
		file.ResidentMemories[idx].Content = content
		file.ResidentMemories[idx].UpdatedAt = now
		if file.ResidentMemories[idx].CreatedAt == "" {
			file.ResidentMemories[idx].CreatedAt = now
		}
	}
	return s.saveLocked(file)
}

func (s *Store) Delete(ctx context.Context, scope session.Scope) error {
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
	idx := findMemory(file.ResidentMemories, scope)
	if idx < 0 {
		return ErrNotFound
	}
	file.ResidentMemories = append(file.ResidentMemories[:idx], file.ResidentMemories[idx+1:]...)
	return s.saveLocked(file)
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
