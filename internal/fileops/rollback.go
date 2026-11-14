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

	"elbot/internal/operationresult"
)

const (
	rollbackMaxBytes   = 256 * 1024 * 1024
	rollbackMaxRecords = 1024
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

type Binding interface{ Valid() bool }

type rollbackScope struct {
	binding Binding
	files   map[string]*rollbackRecord
}

type rollbackLock struct {
	token chan struct{}
	users int
}

// RollbackManager owns bounded, process-local backups. It does not own session lifecycle.
type RollbackManager struct {
	mu         sync.Mutex
	scopes     map[Binding]*rollbackScope
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
		scopes:     make(map[Binding]*rollbackScope),
		records:    make(map[uint64]*rollbackRecord),
		locks:      make(map[string]*rollbackLock),
		maxBytes:   rollbackMaxBytes,
		maxRecords: rollbackMaxRecords,
	}
}

// Forget releases only the records owned by this activation.
func (m *RollbackManager) Forget(binding Binding) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forgetLocked(binding)
}

func (m *RollbackManager) forgetLocked(binding Binding) {
	scope := m.scopes[binding]
	if scope == nil {
		return
	}
	for _, record := range scope.files {
		m.removeLocked(record)
	}
	delete(m.scopes, binding)
}

// Session captures the publisher-owned binding; it never creates validity.
func (m *RollbackManager) Session(binding Binding) (*RollbackSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if binding == nil || !binding.Valid() {
		return nil, false
	}
	// Also reclaim missed cleanup events when the next activation is used.
	for old := range m.scopes {
		if !old.Valid() {
			m.forgetLocked(old)
		}
	}
	scope := m.scopes[binding]
	if scope == nil {
		scope = &rollbackScope{binding: binding, files: make(map[string]*rollbackRecord)}
		m.scopes[binding] = scope
	}
	return &RollbackSession{manager: m, scope: scope}, true
}

func (s *RollbackSession) validLocked() bool {
	return s.scope.binding.Valid() && s.manager.scopes[s.scope.binding] == s.scope
}

func (s *RollbackSession) check(ctx context.Context) (operationresult.Result[struct{}], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	if !s.validLocked() {
		return operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "rollback session expired"}, nil
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (s *RollbackSession) List() operationresult.Result[[]RollbackInfo] {
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	if !s.validLocked() {
		return operationresult.Result[[]RollbackInfo]{Status: operationresult.Conflict, Message: "rollback session expired"}
	}
	infos := make([]RollbackInfo, 0, len(s.scope.files))
	for _, record := range s.scope.files {
		infos = append(infos, record.RollbackInfo)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return operationresult.Result[[]RollbackInfo]{Status: operationresult.Success, Data: infos}
}

// EditFile captures the edit's own bytes and publishes a backup only after a successful write.
func (s *RollbackSession) EditFile(ctx context.Context, path, encoding, revision string, create bool, contextLines int, edits []Edit, options EditFileOptions, checkWrite func(string) operationresult.Result[struct{}]) (operationresult.Result[EditResult], error) {
	return s.manager.editFile(ctx, s, path, encoding, revision, create, contextLines, edits, options, checkWrite, commitGuard{}, nil)
}

func (m *RollbackManager) editFile(ctx context.Context, s *RollbackSession, path, encoding, revision string, create bool, contextLines int, edits []Edit, options EditFileOptions, checkWrite func(string) operationresult.Result[struct{}], guard commitGuard, prepared *EditResult) (operationresult.Result[EditResult], error) {
	if s != nil {
		checked, err := s.check(ctx)
		if err != nil {
			return operationresult.Result[EditResult]{}, err
		}
		if checked.Status != operationresult.Success {
			return operationresult.Result[EditResult]{Status: checked.Status, Message: checked.Message}, nil
		}
	} else if err := ctx.Err(); err != nil {
		return operationresult.Result[EditResult]{}, err
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return operationresult.Result[EditResult]{}, err
	}
	resolved, err := ResolveFileTarget(path, create)
	if err != nil {
		return operationresult.Result[EditResult]{}, err
	}
	if resolved.Status != operationresult.Success {
		return operationresult.Result[EditResult]{Status: resolved.Status, Message: resolved.Message}, nil
	}
	target := resolved.Data
	unlock, err := m.lockTarget(ctx, target)
	if err != nil {
		return operationresult.Result[EditResult]{}, err
	}
	defer unlock()
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	validate := options.beforeWrite
	var before File
	var mode os.FileMode
	options.beforeWrite = func(file File, created bool, originalMode os.FileMode) (operationresult.Result[struct{}], error) {
		snapshot, err := inspectFileState(path, created, ContentRevision(file.Bytes), int64(len(file.Bytes)))
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		if snapshot.Status != operationresult.Success {
			return operationresult.Result[struct{}]{Status: snapshot.Status, Message: snapshot.Message}, nil
		}
		if validate != nil {
			checked, err := validate(file, created, originalMode)
			if err != nil || checked.Status != operationresult.Success {
				return checked, err
			}
		}
		admitted, err := guard.enter(ctx)
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		if admitted.Status != operationresult.Success {
			return operationresult.Result[struct{}]{Status: admitted.Status, Message: admitted.Message}, nil
		}
		release, ctx = admitted.Data.Release, admitted.Data.Context
		if s != nil {
			checked, err := s.check(ctx)
			if err != nil || checked.Status != operationresult.Success {
				return checked, err
			}
		}
		checked, err := checkFileTarget(path, target, created)
		if err != nil || checked.Status != operationresult.Success {
			return checked, err
		}
		if checkWrite != nil {
			for _, checkedPath := range []string{path, target} {
				checked := checkWrite(checkedPath)
				if checked.Status != operationresult.Success {
					return checked, nil
				}
			}
		}
		checked, err = checkFileState(path, created, snapshot.Data)
		if err != nil || checked.Status != operationresult.Success {
			return checked, err
		}
		if err := ctx.Err(); err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		before, mode = file, originalMode
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}
	committed, err := editFileWithResult(path, encoding, revision, create, false, contextLines, edits, options, prepared)
	if err != nil {
		return operationresult.Result[EditResult]{}, err
	}
	if committed.Status != operationresult.Success {
		return committed, nil
	}
	result := committed.Data
	if s == nil {
		return committed, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !s.validLocked() {
		// The write may finish after a session switch; never resurrect its backup.
		return operationresult.Result[EditResult]{Status: operationresult.Success, Data: result}, nil
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
	return operationresult.Result[EditResult]{Status: operationresult.Success, Data: result}, nil
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
	Diff          string
	DiffAvailable bool // Diff 是否包含可展示的完整差异 hunk。
}

type RollbackResult struct {
	Path     string
	Deleted  bool
	Revision string
}

// Preview checks the same preconditions as Rollback without changing the file.
func (s *RollbackSession) Preview(ctx context.Context, path string, expectedID uint64, checkWrite func(string) operationresult.Result[struct{}]) (operationresult.Result[RollbackPreview], error) {
	prepared, current, unlock, err := s.prepare(ctx, path, expectedID, checkWrite)
	if err != nil {
		return operationresult.Result[RollbackPreview]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[RollbackPreview]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	defer unlock()
	record := prepared.Data
	diff := omittedDiff(record.Path, "file exceeds detailed diff limit")
	diffAvailable := false
	if len(current) <= MaxFileSize && len(record.before) <= MaxFileSize {
		newText, _, _, decodeErr := DecodeBytes(current, record.encoding)
		oldText, _, _, oldErr := DecodeBytes(record.before, record.encoding)
		if decodeErr == nil && oldErr == nil {
			diff, diffAvailable = editDiff(record.Path, NormalizeEditText(newText), NormalizeEditText(oldText), len(current), len(record.before), 3, MaxFileSize)
		}
	}
	return operationresult.Result[RollbackPreview]{Status: operationresult.Success, Data: RollbackPreview{RollbackInfo: record.RollbackInfo, Diff: diff, DiffAvailable: diffAvailable}}, nil
}

func (s *RollbackSession) Rollback(ctx context.Context, path string, expectedID uint64, checkWrite func(string) operationresult.Result[struct{}]) (operationresult.Result[RollbackResult], error) {
	return s.rollback(ctx, path, expectedID, checkWrite, commitGuard{})
}

func (s *RollbackSession) rollback(ctx context.Context, path string, expectedID uint64, checkWrite func(string) operationresult.Result[struct{}], guard commitGuard) (operationresult.Result[RollbackResult], error) {
	prepared, _, unlock, err := s.prepare(ctx, path, expectedID, checkWrite)
	if err != nil {
		return operationresult.Result[RollbackResult]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[RollbackResult]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	defer unlock()
	record := prepared.Data
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	m := s.manager
	held := false
	defer func() {
		if held {
			m.mu.Unlock()
		}
	}()
	beforeCommit := func() (operationresult.Result[struct{}], error) {
		snapshot, err := inspectFileState(record.Target, false, record.RevisionAfter, record.afterSize)
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		if snapshot.Status != operationresult.Success {
			return operationresult.Result[struct{}]{Status: snapshot.Status, Message: snapshot.Message}, nil
		}
		admitted, err := guard.enter(ctx)
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		if admitted.Status != operationresult.Success {
			return operationresult.Result[struct{}]{Status: admitted.Status, Message: admitted.Message}, nil
		}
		release = admitted.Data.Release
		ctx = admitted.Data.Context
		if checked, err := checkRollbackFileTarget(path, record.Target); err != nil || checked.Status != operationresult.Success {
			return checked, err
		}
		if checked, err := checkRollbackFileTarget(record.Path, record.Target); err != nil || checked.Status != operationresult.Success {
			return checked, err
		}
		if checkWrite != nil {
			for _, p := range []string{path, record.Path, record.Target} {
				checked := checkWrite(p)
				if checked.Status != operationresult.Success {
					return checked, nil
				}
			}
		}
		if checked, err := checkFileState(record.Target, false, snapshot.Data); err != nil || checked.Status != operationresult.Success {
			return checked, err
		}
		m.mu.Lock()
		held = true
		if !s.validLocked() {
			return operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "rollback session expired"}, nil
		}
		if m.records[record.ID] != record {
			return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: "no rollback record: it may have been consumed, replaced, or evicted"}, nil
		}
		if err := ctx.Err(); err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}
	if record.Created {
		commit, commitErr := beforeCommit()
		if commitErr != nil {
			err = commitErr
		} else if commit.Status != operationresult.Success {
			return operationresult.Result[RollbackResult]{Status: commit.Status, Message: commit.Message}, nil
		} else {
			err = os.Remove(record.Target)
		}
	} else {
		commit, commitErr := atomicWriteFileResult(record.Target, record.before, record.mode, beforeCommit)
		if commitErr != nil {
			err = commitErr
		} else if commit.Status != operationresult.Success {
			return operationresult.Result[RollbackResult]{Status: commit.Status, Message: commit.Message}, nil
		}
	}
	if err != nil {
		return operationresult.Result[RollbackResult]{}, fmt.Errorf("rollback file: %w", err)
	}
	m.removeLocked(record)
	result := RollbackResult{Path: record.Path, Deleted: record.Created}
	if !record.Created {
		result.Revision = record.RevisionBefore
	}
	return operationresult.Result[RollbackResult]{Status: operationresult.Success, Data: result}, nil
}

func (s *RollbackSession) prepare(ctx context.Context, path string, expectedID uint64, checkWrite func(string) operationresult.Result[struct{}]) (operationresult.Result[*rollbackRecord], []byte, func(), error) {
	checked, err := s.check(ctx)
	if err != nil {
		return operationresult.Result[*rollbackRecord]{}, nil, nil, err
	}
	if checked.Status != operationresult.Success {
		return operationresult.Result[*rollbackRecord]{Status: checked.Status, Message: checked.Message}, nil, nil, nil
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return operationresult.Result[*rollbackRecord]{}, nil, nil, err
	}
	if checkWrite != nil {
		checked := checkWrite(path)
		if checked.Status != operationresult.Success {
			return operationresult.Result[*rollbackRecord]{Status: checked.Status, Message: checked.Message}, nil, nil, nil
		}
	}
	resolved, err := ResolveFileTarget(path, false)
	if err != nil {
		return operationresult.Result[*rollbackRecord]{}, nil, nil, fmt.Errorf("rollback target unavailable: %w", err)
	}
	if resolved.Status == operationresult.NotFound {
		return operationresult.Result[*rollbackRecord]{Status: operationresult.NotFound, Message: "no rollback record: it may have been consumed, replaced, or evicted"}, nil, nil, nil
	}
	if resolved.Status != operationresult.Success {
		return operationresult.Result[*rollbackRecord]{Status: resolved.Status, Message: resolved.Message}, nil, nil, nil
	}
	target := resolved.Data
	unlock, err := s.manager.lockTarget(ctx, target)
	if err != nil {
		return operationresult.Result[*rollbackRecord]{}, nil, nil, err
	}
	record, current, err := s.loadRecord(ctx, path, target, expectedID, checkWrite)
	if err != nil {
		unlock()
		return operationresult.Result[*rollbackRecord]{}, nil, nil, err
	}
	if record.Status != operationresult.Success {
		unlock()
		return record, nil, nil, nil
	}
	return record, current, unlock, nil
}

func (s *RollbackSession) loadRecord(ctx context.Context, path, target string, expectedID uint64, checkWrite func(string) operationresult.Result[struct{}]) (operationresult.Result[*rollbackRecord], []byte, error) {
	m := s.manager
	m.mu.Lock()
	if !s.validLocked() {
		m.mu.Unlock()
		return operationresult.Result[*rollbackRecord]{Status: operationresult.Conflict, Message: "rollback session expired"}, nil, nil
	}
	for _, candidate := range s.scope.files {
		if candidate.Path == path && candidate.Target != target {
			m.mu.Unlock()
			return operationresult.Result[*rollbackRecord]{Status: operationresult.Conflict, Message: "file target changed; rollback refused"}, nil, nil
		}
	}
	record := s.scope.files[target]
	m.mu.Unlock()
	if record == nil || (expectedID != 0 && record.ID != expectedID) {
		return operationresult.Result[*rollbackRecord]{Status: operationresult.NotFound, Message: "no rollback record: it may have been consumed, replaced, or evicted"}, nil, nil
	}
	for _, checkedPath := range []string{path, record.Path} {
		checked, err := checkRollbackFileTarget(checkedPath, record.Target)
		if err != nil {
			return operationresult.Result[*rollbackRecord]{}, nil, err
		}
		if checked.Status != operationresult.Success {
			return operationresult.Result[*rollbackRecord]{Status: checked.Status, Message: checked.Message}, nil, nil
		}
	}
	if checkWrite != nil {
		for _, checkedPath := range []string{record.Path, record.Target} {
			checked := checkWrite(checkedPath)
			if checked.Status != operationresult.Success {
				return operationresult.Result[*rollbackRecord]{Status: checked.Status, Message: checked.Message}, nil, nil
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*rollbackRecord]{}, nil, err
	}
	// Avoid loading arbitrarily large externally modified files into memory.
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return operationresult.Result[*rollbackRecord]{Status: operationresult.NotFound, Message: "no rollback record: it may have been consumed, replaced, or evicted"}, nil, nil
		}
		return operationresult.Result[*rollbackRecord]{}, nil, err
	}
	if !info.Mode().IsRegular() {
		return operationresult.Result[*rollbackRecord]{Status: operationresult.InvalidTarget, Message: "rollback target is not a regular file"}, nil, nil
	}
	if info.Size() != record.afterSize {
		return operationresult.Result[*rollbackRecord]{Status: operationresult.Conflict, Message: "file changed after edit; rollback refused"}, nil, nil
	}
	current, err := readRevisionBytes(target, record.afterSize)
	if err != nil {
		return operationresult.Result[*rollbackRecord]{}, nil, err
	}
	if current.Status == operationresult.NotFound {
		return operationresult.Result[*rollbackRecord]{Status: operationresult.NotFound, Message: "no rollback record: it may have been consumed, replaced, or evicted"}, nil, nil
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[*rollbackRecord]{Status: current.Status, Message: current.Message}, nil, nil
	}
	if ContentRevision(current.Data) != record.RevisionAfter {
		return operationresult.Result[*rollbackRecord]{Status: operationresult.Conflict, Message: "file changed after edit; rollback refused"}, nil, nil
	}
	return operationresult.Result[*rollbackRecord]{Status: operationresult.Success, Data: record}, current.Data, nil
}

func readRevisionBytes(path string, size int64) (operationresult.Result[[]byte], error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return operationresult.Result[[]byte]{Status: operationresult.NotFound, Message: fmt.Sprintf("file %q not found", path)}, nil
	}
	if err != nil {
		return operationresult.Result[[]byte]{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, size+1))
	if err != nil {
		return operationresult.Result[[]byte]{}, err
	}
	if int64(len(data)) != size {
		return operationresult.Result[[]byte]{Status: operationresult.Conflict, Message: "file changed while reading revision"}, nil
	}
	return operationresult.Result[[]byte]{Status: operationresult.Success, Data: data}, nil
}

// ResolveFileTarget resolves symlinks, including existing parents of a new file.
func ResolveFileTarget(path string, allowCreate bool) (operationresult.Result[string], error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	target, err := filepath.EvalSymlinks(path)
	if err == nil {
		info, statErr := os.Stat(target)
		if errors.Is(statErr, os.ErrNotExist) {
			return operationresult.Result[string]{Status: operationresult.NotFound, Message: statErr.Error()}, nil
		}
		if statErr != nil {
			return operationresult.Result[string]{}, statErr
		}
		if !info.Mode().IsRegular() {
			return operationresult.Result[string]{Status: operationresult.InvalidTarget, Message: "target is not a regular file"}, nil
		}
		return operationresult.Result[string]{Status: operationresult.Success, Data: filepath.Clean(target)}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return operationresult.Result[string]{}, err
	}
	if !allowCreate {
		return operationresult.Result[string]{Status: operationresult.NotFound, Message: err.Error()}, nil
	}
	// Do not interpret a dangling final symlink as a new file.
	if _, lstatErr := os.Lstat(path); lstatErr == nil {
		return operationresult.Result[string]{Status: operationresult.InvalidTarget, Message: err.Error()}, nil
	} else if !errors.Is(lstatErr, os.ErrNotExist) {
		return operationresult.Result[string]{}, lstatErr
	}
	parent := filepath.Dir(path)
	suffix := filepath.Base(path)
	for {
		resolved, resolveErr := filepath.EvalSymlinks(parent)
		if resolveErr == nil {
			return operationresult.Result[string]{Status: operationresult.Success, Data: filepath.Join(resolved, suffix)}, nil
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return operationresult.Result[string]{}, resolveErr
		}
		if filepath.Dir(parent) == parent {
			return operationresult.Result[string]{Status: operationresult.NotFound, Message: resolveErr.Error()}, nil
		}
		if _, lstatErr := os.Lstat(parent); lstatErr == nil {
			return operationresult.Result[string]{Status: operationresult.InvalidTarget, Message: resolveErr.Error()}, nil
		} else if !errors.Is(lstatErr, os.ErrNotExist) {
			return operationresult.Result[string]{}, lstatErr
		}
		suffix = filepath.Join(filepath.Base(parent), suffix)
		parent = filepath.Dir(parent)
	}
}

func checkFileTarget(path, target string, allowCreate bool) (operationresult.Result[struct{}], error) {
	current, err := ResolveFileTarget(path, allowCreate)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[struct{}]{Status: current.Status, Message: current.Message}, nil
	}
	if current.Data != target {
		return operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "file target changed; operation refused"}, nil
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func checkRollbackFileTarget(path, target string) (operationresult.Result[struct{}], error) {
	checked, err := checkFileTarget(path, target, false)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if checked.Status == operationresult.NotFound {
		return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: "no rollback record: it may have been consumed, replaced, or evicted"}, nil
	}
	return checked, nil
}

// inspectFileState reads bytes before Session admission; final commit rechecks only file identity and stat information.
func inspectFileState(path string, created bool, revision string, size int64) (operationresult.Result[os.FileInfo], error) {
	if created {
		checked, err := checkFileState(path, true, nil)
		if err != nil {
			return operationresult.Result[os.FileInfo]{}, err
		}
		return operationresult.Result[os.FileInfo]{Status: checked.Status, Message: checked.Message}, nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return operationresult.Result[os.FileInfo]{Status: operationresult.NotFound, Message: err.Error()}, nil
	}
	if err != nil {
		return operationresult.Result[os.FileInfo]{}, err
	}
	if !info.Mode().IsRegular() {
		return operationresult.Result[os.FileInfo]{Status: operationresult.InvalidTarget, Message: "target is not a regular file"}, nil
	}
	if info.Size() != size {
		return operationresult.Result[os.FileInfo]{Status: operationresult.Conflict, Message: "file changed before commit"}, nil
	}
	data, err := readRevisionBytes(path, size)
	if err != nil {
		return operationresult.Result[os.FileInfo]{}, err
	}
	if data.Status != operationresult.Success {
		return operationresult.Result[os.FileInfo]{Status: data.Status, Message: data.Message}, nil
	}
	if ContentRevision(data.Data) != revision {
		return operationresult.Result[os.FileInfo]{Status: operationresult.Conflict, Message: "file changed before commit"}, nil
	}
	checked, err := checkFileState(path, false, info)
	if err != nil {
		return operationresult.Result[os.FileInfo]{}, err
	}
	if checked.Status != operationresult.Success {
		return operationresult.Result[os.FileInfo]{Status: checked.Status, Message: checked.Message}, nil
	}
	return operationresult.Result[os.FileInfo]{Status: operationresult.Success, Data: info}, nil
}

func checkFileState(path string, created bool, previous os.FileInfo) (operationresult.Result[struct{}], error) {
	if created {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
		} else if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		return operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "file appeared after preflight; edit refused"}, nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: err.Error()}, nil
	}
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if !info.Mode().IsRegular() {
		return operationresult.Result[struct{}]{Status: operationresult.InvalidTarget, Message: "target is not a regular file"}, nil
	}
	if !os.SameFile(previous, info) || info.Size() != previous.Size() || info.Mode() != previous.Mode() || !info.ModTime().Equal(previous.ModTime()) {
		return operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "file changed before commit"}, nil
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}
