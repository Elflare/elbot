package fileops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	rollbackMaxBytes   = 256 * 1024 * 1024
	rollbackMaxRecords = 1024
)

var (
	ErrRollbackExpired  = errors.New("rollback session expired")
	ErrRollbackNotFound = errors.New("no rollback record: it may have been consumed, replaced, or evicted")
)

// RollbackInfo is safe to return to callers; file contents stay inside the manager.
type RollbackInfo struct {
	ID             uint64
	Path           string
	Target         string
	Created        bool
	EditedAt       time.Time
	RevisionBefore string
	RevisionAfter  string
}

type rollbackRecord struct {
	RollbackInfo
	scope     *rollbackScope
	before    []byte
	mode      os.FileMode
	encoding  string
	afterSize int64
}

type rollbackScope struct {
	key       string
	sessionID string
	files     map[string]*rollbackRecord
}

type rollbackLock struct {
	token chan struct{}
	users int
}

// RollbackManager owns bounded, process-local backups. It does not own session lifecycle.
type RollbackManager struct {
	mu         sync.Mutex
	scopes     map[string]*rollbackScope
	records    map[uint64]*rollbackRecord
	locks      map[string]*rollbackLock
	nextID     uint64
	bytes      int64
	maxBytes   int64
	maxRecords int
}

// RollbackSession is a lease for one activation of a session. Switching away
// permanently invalidates old leases, even if the same session is later resumed.
type RollbackSession struct {
	manager *RollbackManager
	scope   *rollbackScope
}

func NewRollbackManager() *RollbackManager {
	return &RollbackManager{
		scopes:     make(map[string]*rollbackScope),
		records:    make(map[uint64]*rollbackRecord),
		locks:      make(map[string]*rollbackLock),
		maxBytes:   rollbackMaxBytes,
		maxRecords: rollbackMaxRecords,
	}
}

// SetCurrent must be called in session-transition order. It performs no file I/O.
func (m *RollbackManager) SetCurrent(scope, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.scopes[scope]
	if old != nil && old.sessionID == sessionID {
		return
	}
	if old != nil {
		for _, record := range old.files {
			m.removeLocked(record)
		}
		delete(m.scopes, scope)
	}
	if sessionID != "" {
		m.scopes[scope] = &rollbackScope{key: scope, sessionID: sessionID, files: make(map[string]*rollbackRecord)}
	}
}

func (m *RollbackManager) Session(scope, sessionID string) (*RollbackSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.scopes[scope]
	if current == nil || current.sessionID != sessionID {
		return nil, false
	}
	return &RollbackSession{manager: m, scope: current}, true
}

func (s *RollbackSession) validLocked() bool {
	return s.manager.scopes[s.scope.key] == s.scope
}

func (s *RollbackSession) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	if !s.validLocked() {
		return ErrRollbackExpired
	}
	return nil
}

func (s *RollbackSession) List() ([]RollbackInfo, error) {
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	if !s.validLocked() {
		return nil, ErrRollbackExpired
	}
	infos := make([]RollbackInfo, 0, len(s.scope.files))
	for _, record := range s.scope.files {
		infos = append(infos, record.RollbackInfo)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos, nil
}

// EditFile captures bytes from the edit's own read and publishes a backup only
// after the write succeeds. The caller must check access to both path and target.
func (s *RollbackSession) EditFile(ctx context.Context, path, encoding, revision string, create bool, contextLines int, edits []Edit, options EditFileOptions, checkWrite func(string) error) (EditResult, error) {
	if err := s.check(ctx); err != nil {
		return EditResult{}, err
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return EditResult{}, err
	}
	target, err := ResolveFileTarget(path, create)
	if err != nil {
		return EditResult{}, err
	}
	unlock, err := s.manager.lockTarget(ctx, target)
	if err != nil {
		return EditResult{}, err
	}
	defer unlock()

	var before File
	var mode os.FileMode
	options.beforeWrite = func(file File, created bool, originalMode os.FileMode) error {
		if err := s.check(ctx); err != nil {
			return err
		}
		if err := checkFileTarget(path, target, created); err != nil {
			return err
		}
		if checkWrite != nil {
			for _, checkedPath := range []string{path, target} {
				if err := checkWrite(checkedPath); err != nil {
					return err
				}
			}
		}
		before, mode = file, originalMode
		return nil
	}
	result, err := EditFileWithOptions(path, encoding, revision, create, false, contextLines, edits, options)
	if err != nil {
		return EditResult{}, err
	}

	m := s.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if !s.validLocked() {
		// The write may finish after a session switch; never resurrect its backup.
		return result, nil
	}
	if previous := s.scope.files[target]; previous != nil {
		m.removeLocked(previous)
	}
	m.nextID++
	record := &rollbackRecord{
		RollbackInfo: RollbackInfo{ID: m.nextID, Path: path, Target: target, Created: result.Created,
			EditedAt: time.Now(), RevisionBefore: result.RevisionBefore, RevisionAfter: result.RevisionAfter},
		scope: s.scope, before: before.Bytes, mode: mode, encoding: before.Encoding, afterSize: int64(len(result.NewBytes)),
	}
	m.records[record.ID] = record
	s.scope.files[target] = record
	m.bytes += int64(len(record.before))
	for m.bytes > m.maxBytes || len(m.records) > m.maxRecords {
		var oldest *rollbackRecord
		for _, candidate := range m.records {
			if oldest == nil || candidate.ID < oldest.ID {
				oldest = candidate
			}
		}
		m.removeLocked(oldest)
		result.RollbackEvicted++
	}
	result.RollbackAvailable = m.records[record.ID] == record
	return result, nil
}

func (m *RollbackManager) removeLocked(record *rollbackRecord) {
	delete(m.records, record.ID)
	delete(record.scope.files, record.Target)
	m.bytes -= int64(len(record.before))
}

func (m *RollbackManager) lockTarget(ctx context.Context, target string) (func(), error) {
	m.mu.Lock()
	lock := m.locks[target]
	if lock == nil {
		lock = &rollbackLock{token: make(chan struct{}, 1)}
		m.locks[target] = lock
	}
	lock.users++
	m.mu.Unlock()
	release := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		lock.users--
		if lock.users == 0 {
			delete(m.locks, target)
		}
	}
	select {
	case lock.token <- struct{}{}:
		return func() { <-lock.token; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

type RollbackPreview struct {
	RollbackInfo
	Diff string
}

type RollbackResult struct {
	Path     string
	Deleted  bool
	Revision string
}

// Preview checks the same preconditions as Rollback without changing the file.
func (s *RollbackSession) Preview(ctx context.Context, path string, expectedID uint64, checkWrite func(string) error) (RollbackPreview, error) {
	record, current, unlock, err := s.prepare(ctx, path, expectedID, checkWrite)
	if err != nil {
		return RollbackPreview{}, err
	}
	defer unlock()
	diff := omittedDiff(record.Path, "file exceeds detailed diff limit")
	if len(current) <= MaxFileSize && len(record.before) <= MaxFileSize {
		newText, _, _, decodeErr := DecodeBytes(current, record.encoding)
		oldText, _, _, oldErr := DecodeBytes(record.before, record.encoding)
		if decodeErr == nil && oldErr == nil {
			diff = editDiff(record.Path, NormalizeEditText(newText), NormalizeEditText(oldText), len(current), len(record.before), 3, MaxFileSize)
		}
	}
	return RollbackPreview{RollbackInfo: record.RollbackInfo, Diff: diff}, nil
}

func (s *RollbackSession) Rollback(ctx context.Context, path string, expectedID uint64, checkWrite func(string) error) (RollbackResult, error) {
	record, _, unlock, err := s.prepare(ctx, path, expectedID, checkWrite)
	if err != nil {
		return RollbackResult{}, err
	}
	defer unlock()
	// Keep lifecycle invalidation and eviction from racing the final write.
	m := s.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if !s.validLocked() {
		return RollbackResult{}, ErrRollbackExpired
	}
	if m.records[record.ID] != record {
		return RollbackResult{}, ErrRollbackNotFound
	}
	if err := ctx.Err(); err != nil {
		return RollbackResult{}, err
	}
	if record.Created {
		err = os.Remove(record.Target)
	} else {
		err = AtomicWriteFile(record.Target, record.before, record.mode)
	}
	if err != nil {
		return RollbackResult{}, fmt.Errorf("rollback file: %w", err)
	}
	m.removeLocked(record)
	result := RollbackResult{Path: record.Path, Deleted: record.Created}
	if !record.Created {
		result.Revision = record.RevisionBefore
	}
	return result, nil
}

func (s *RollbackSession) prepare(ctx context.Context, path string, expectedID uint64, checkWrite func(string) error) (*rollbackRecord, []byte, func(), error) {
	if err := s.check(ctx); err != nil {
		return nil, nil, nil, err
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, nil, err
	}
	if checkWrite != nil {
		if err := checkWrite(path); err != nil {
			return nil, nil, nil, err
		}
	}
	target, err := ResolveFileTarget(path, false)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("rollback target unavailable: %w", err)
	}
	unlock, err := s.manager.lockTarget(ctx, target)
	if err != nil {
		return nil, nil, nil, err
	}
	record, current, err := s.loadRecord(ctx, path, target, expectedID, checkWrite)
	if err != nil {
		unlock()
		return nil, nil, nil, err
	}
	return record, current, unlock, nil
}

func (s *RollbackSession) loadRecord(ctx context.Context, path, target string, expectedID uint64, checkWrite func(string) error) (*rollbackRecord, []byte, error) {
	m := s.manager
	m.mu.Lock()
	if !s.validLocked() {
		m.mu.Unlock()
		return nil, nil, ErrRollbackExpired
	}
	for _, candidate := range s.scope.files {
		if candidate.Path == path && candidate.Target != target {
			m.mu.Unlock()
			return nil, nil, fmt.Errorf("file target changed; rollback refused")
		}
	}
	record := s.scope.files[target]
	m.mu.Unlock()
	if record == nil || (expectedID != 0 && record.ID != expectedID) {
		return nil, nil, ErrRollbackNotFound
	}
	for _, checkedPath := range []string{path, record.Path} {
		if err := checkFileTarget(checkedPath, record.Target, false); err != nil {
			return nil, nil, err
		}
	}
	if checkWrite != nil {
		for _, checkedPath := range []string{record.Path, record.Target} {
			if err := checkWrite(checkedPath); err != nil {
				return nil, nil, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	// Avoid loading arbitrarily large externally modified files into memory.
	info, err := os.Stat(target)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("rollback target is not a regular file")
	}
	if info.Size() != record.afterSize {
		return nil, nil, fmt.Errorf("file changed after edit; rollback refused")
	}
	current, err := readRevisionBytes(target, record.afterSize)
	if err != nil {
		return nil, nil, err
	}
	if ContentRevision(current) != record.RevisionAfter {
		return nil, nil, fmt.Errorf("file changed after edit; rollback refused")
	}
	return record, current, nil
}

func readRevisionBytes(path string, size int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, fmt.Errorf("file changed after edit; rollback refused")
	}
	return data, nil
}

// ResolveFileTarget resolves symlinks, including existing parents of a new file.
func ResolveFileTarget(path string, allowCreate bool) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(path)
	if err == nil {
		info, statErr := os.Stat(target)
		if statErr != nil {
			return "", statErr
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("path is not a regular file")
		}
		return filepath.Clean(target), nil
	}
	if !allowCreate || !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// Do not interpret a dangling final symlink as a new file.
	if _, lstatErr := os.Lstat(path); lstatErr == nil {
		return "", err
	}
	parent := filepath.Dir(path)
	suffix := filepath.Base(path)
	for {
		resolved, resolveErr := filepath.EvalSymlinks(parent)
		if resolveErr == nil {
			return filepath.Join(resolved, suffix), nil
		}
		if !errors.Is(resolveErr, os.ErrNotExist) || filepath.Dir(parent) == parent {
			return "", resolveErr
		}
		if _, lstatErr := os.Lstat(parent); lstatErr == nil {
			return "", resolveErr
		}
		suffix = filepath.Join(filepath.Base(parent), suffix)
		parent = filepath.Dir(parent)
	}
}

func checkFileTarget(path, target string, allowCreate bool) error {
	current, err := ResolveFileTarget(path, allowCreate)
	if err != nil {
		return err
	}
	if current != target {
		return fmt.Errorf("file target changed; operation refused")
	}
	return nil
}
