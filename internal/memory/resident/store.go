package resident

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"elbot/internal/contextinfo"
	"elbot/internal/session"
	"elbot/internal/storage"
)

const (
	DefaultCoreMaxUnits   = 200
	DefaultNormalMaxUnits = 300
)

var ErrNotFound = errors.New("resident memory not found")

type Limits struct {
	Core   int // 核心记忆允许的最大字数或单词数。
	Normal int // 普通记忆允许的最大字数或单词数。
}

type Memory struct {
	Core   string `json:"core"`   // 核心记忆；未保存时为空。
	Normal string `json:"normal"` // 普通记忆；未保存时为空。
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
	Owner     contextinfo.ActorIdentity `toml:"owner"` // 平台与参与者 ID，不包含展示名。
	Core      string                    `toml:"core"`
	Normal    string                    `toml:"normal"`
	CreatedAt string                    `toml:"created_at"`
	UpdatedAt string                    `toml:"updated_at"`
}

type tomlFile struct {
	Version          int              `toml:"version"` // 持久化格式版本；2 使用结构化所有者。
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
	file   tomlFile // 锁内共享；修改列表前复制，保存成功后替换。
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

func ActorScope(actor contextinfo.Actor) session.Scope {
	return session.Scope{Owner: actor.ActorIdentity, Platform: actor.Platform}
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
	return s.update(ctx, scope, func(memory *ResidentMemory) error {
		memory.Core = strings.TrimSpace(content)
		return validatePart(memory.Core, s.Limits.Core, "core")
	})
}

func (s *Store) WriteNormal(ctx context.Context, scope session.Scope, content string) error {
	return s.update(ctx, scope, func(memory *ResidentMemory) error {
		memory.Normal = strings.TrimSpace(content)
		return validatePart(memory.Normal, s.Limits.Normal, "normal")
	})
}

func (s *Store) AppendNormal(ctx context.Context, scope session.Scope, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("resident memory normal content is required")
	}
	return s.update(ctx, scope, func(memory *ResidentMemory) error {
		if memory.Normal == "" {
			memory.Normal = content
		} else {
			memory.Normal += " " + content
		}
		return validatePart(memory.Normal, s.Limits.Normal, "normal")
	})
}

func (s *Store) DeleteNormal(ctx context.Context, scope session.Scope) error {
	return s.WriteNormal(ctx, scope, "")
}

func (s *Store) LimitsOrDefault() Limits {
	return s.Limits
}

func (s *Store) update(ctx context.Context, scope session.Scope, update func(*ResidentMemory) error) error {
	if err := ctx.Err(); err != nil {
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
	exists := idx >= 0
	memory := ResidentMemory{Owner: scope.Owner}
	if exists {
		memory = file.ResidentMemories[idx]
	}
	before := memory
	memory.Core = strings.TrimSpace(memory.Core)
	memory.Normal = strings.TrimSpace(memory.Normal)
	if err := update(&memory); err != nil {
		return err
	}
	if memory.Core == "" && memory.Normal == "" {
		if !exists {
			return nil
		}
		file.ResidentMemories = slices.Delete(slices.Clone(file.ResidentMemories), idx, idx+1)
	} else {
		if memory.CreatedAt == "" {
			memory.CreatedAt = now
		}
		if memory == before {
			return nil
		}
		memory.UpdatedAt = now
		file.ResidentMemories = slices.Clone(file.ResidentMemories)
		if exists {
			file.ResidentMemories[idx] = memory
		} else {
			file.ResidentMemories = append(file.ResidentMemories, memory)
		}
	}
	return s.saveLocked(file)
}

func validatePart(content string, limit int, part string) error {
	if units := CountUnits(content); units > limit {
		return fmt.Errorf("resident memory %s is too long: %d/%d units", part, units, limit)
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
		return s.cache.file, nil
	}
	if !state.exists {
		file := tomlFile{Version: 2}
		s.cache = storeCache{loaded: true, file: file, state: state}
		return file, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return tomlFile{}, fmt.Errorf("read resident memory %q: %w", path, err)
	}
	var file tomlFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return tomlFile{}, fmt.Errorf("parse resident memory %q: %w", path, err)
	}
	if file.Version == 0 {
		file, err = migrateResidentMemory(data)
		if err != nil {
			return tomlFile{}, fmt.Errorf("migrate resident memory %q: %w", path, err)
		}
		if err = s.saveLocked(file); err != nil {
			return tomlFile{}, err
		}
		return file, nil
	}
	if file.Version != 2 {
		return tomlFile{}, fmt.Errorf("unsupported resident memory version %d", file.Version)
	}
	s.cache = storeCache{loaded: true, file: file, state: state}
	return file, nil
}

func (s *Store) saveLocked(file tomlFile) error {
	path := strings.TrimSpace(s.Path)
	data, err := toml.Marshal(file)
	if err != nil {
		return fmt.Errorf("marshal resident memory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create resident memory dir %q: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".resident-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	state, err := currentFileState(path)
	if err != nil {
		return err
	}
	s.cache = storeCache{loaded: true, file: file, state: state}
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

func findMemory(memories []ResidentMemory, scope session.Scope) int {
	for i, memory := range memories {
		if memory.Owner == scope.Owner {
			return i
		}
	}
	return -1
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

func migrateResidentMemory(data []byte) (tomlFile, error) {
	var old struct {
		Memories []struct {
			Platform  string `toml:"platform"`
			ActorID   string `toml:"actor_id"`
			Core      string `toml:"core"`
			Normal    string `toml:"normal"`
			CreatedAt string `toml:"created_at"`
			UpdatedAt string `toml:"updated_at"`
		} `toml:"resident_memories"`
	}
	if err := toml.Unmarshal(data, &old); err != nil {
		return tomlFile{}, err
	}
	file := tomlFile{Version: 2}
	seen := map[contextinfo.ActorIdentity]bool{}
	for _, item := range old.Memories {
		platform, id, ok := strings.Cut(item.ActorID, ":")
		if !ok || id == "" || (platform != item.Platform && !(platform == "hook" && item.Platform == "")) {
			return tomlFile{}, fmt.Errorf("ambiguous resident owner platform=%q actor=%q", item.Platform, item.ActorID)
		}
		owner := contextinfo.ActorIdentity{Platform: platform, ID: id}
		if platform == "hook" || platform == "elnis" {
			return tomlFile{}, fmt.Errorf("resident owner %q has no recoverable user or Elwisp identity", item.ActorID)
		}
		if seen[owner] {
			return tomlFile{}, fmt.Errorf("duplicate resident owner %+v", owner)
		}
		seen[owner] = true
		file.ResidentMemories = append(file.ResidentMemories, ResidentMemory{Owner: owner, Core: item.Core, Normal: item.Normal, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt})
	}
	return file, nil
}
