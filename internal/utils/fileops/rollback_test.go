package fileops

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func rollbackTestSession(t *testing.T) (*RollbackManager, *RollbackSession) {
	t.Helper()
	m := NewRollbackManager()
	m.SetCurrent("user", "session")
	s, ok := m.Session("user", "session")
	if !ok {
		t.Fatal("missing session")
	}
	return m, s
}

func rollbackTestEdit(t *testing.T, s *RollbackSession, path, text string, create bool) EditResult {
	t.Helper()
	var revision string
	if data, err := os.ReadFile(path); err == nil {
		revision = ContentRevision(data)
	}
	result, err := s.EditFile(context.Background(), path, "", revision, create, 3,
		[]Edit{{Operation: "overwrite", NewText: &text}}, EditFileOptions{MaxInputBytes: MaxFileSize}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRollbackRestoresBytesAndPermissions(t *testing.T) {
	for _, original := range [][]byte{
		[]byte("before\r\nline\r\n"),
		[]byte("before\nline"),
		append([]byte{0xef, 0xbb, 0xbf}, []byte("before\n")...),
		{0xff, 0xfe, 'a', 0, '\r', 0, '\n', 0},
	} {
		t.Run(ContentRevision(original), func(t *testing.T) {
			_, s := rollbackTestSession(t)
			path := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if result := rollbackTestEdit(t, s, path, "after\n", false); !result.RollbackAvailable {
				t.Fatal("backup unavailable")
			}
			preview, err := s.Preview(context.Background(), path, 0, nil)
			if err != nil || preview.Diff == "" {
				t.Fatalf("preview: %+v %v", preview, err)
			}
			result, err := s.Rollback(context.Background(), path, preview.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.Deleted || result.Revision != ContentRevision(original) {
				t.Fatalf("result: %+v", result)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("bytes %q, err %v", got, err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
				t.Fatalf("mode: %v", info.Mode())
			}
			if _, err := s.Rollback(context.Background(), path, 0, nil); !errors.Is(err, ErrRollbackNotFound) {
				t.Fatalf("second rollback: %v", err)
			}
		})
	}
}

func TestRollbackLatestEditAndStableIDs(t *testing.T) {
	_, s := rollbackTestSession(t)
	dir := t.TempDir()
	path, other := filepath.Join(dir, "one"), filepath.Join(dir, "two")
	rollbackTestEdit(t, s, path, "one", true)
	first, _ := s.List()
	rollbackTestEdit(t, s, other, "other", true)
	rollbackTestEdit(t, s, path, "two", false)
	if _, err := s.Rollback(context.Background(), path, first[0].ID, nil); !errors.Is(err, ErrRollbackNotFound) {
		t.Fatalf("stale ID: %v", err)
	}
	if _, err := s.Rollback(context.Background(), path, 0, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "one" {
		t.Fatalf("restored %q", data)
	}
	remaining, _ := s.List()
	if len(remaining) != 1 || remaining[0].Path != other {
		t.Fatalf("remaining: %+v", remaining)
	}
	if _, err := s.Rollback(context.Background(), other, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created file still exists: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("parent removed: %v", err)
	}
}

func TestRollbackPreviewAndFailedEditsPreserveBackup(t *testing.T) {
	_, s := rollbackTestSession(t)
	path := filepath.Join(t.TempDir(), "file")
	rollbackTestEdit(t, s, path, "one", true)
	before, _ := s.List()
	same := "one"
	_, err := s.EditFile(context.Background(), path, "", ContentRevision([]byte("one")), false, 3, []Edit{{Operation: "overwrite", NewText: &same}}, EditFileOptions{}, nil)
	if err == nil {
		t.Fatal("no-op accepted")
	}
	next := "two"
	if _, err := EditFile(path, "", ContentRevision([]byte("one")), false, true, 3, []Edit{{Operation: "overwrite", NewText: &next}}); err != nil {
		t.Fatal(err)
	}
	_, err = s.EditFile(context.Background(), path, "", ContentRevision([]byte("one")), false, 3, []Edit{{Operation: "overwrite", NewText: &next}}, EditFileOptions{MaxOutputBytes: 1}, nil)
	if err == nil {
		t.Fatal("oversized output accepted")
	}
	after, _ := s.List()
	if len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatalf("backup changed: %+v", after)
	}
	if _, err := s.Rollback(context.Background(), path, 0, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackConflictsAndGuardKeepRecord(t *testing.T) {
	_, s := rollbackTestSession(t)
	path := filepath.Join(t.TempDir(), "file")
	rollbackTestEdit(t, s, path, "one", true)
	for _, changed := range []string{"TWO", "a much larger external edit"} {
		if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Rollback(context.Background(), path, 0, nil); err == nil {
			t.Fatal("external edit overwritten")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rollback(context.Background(), path, 0, nil); err == nil {
		t.Fatal("missing file recreated")
	}
	if err := os.WriteFile(path, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("write denied")
	if _, err := s.Rollback(context.Background(), path, 0, func(string) error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("guard: %v", err)
	}
	records, _ := s.List()
	if len(records) != 1 {
		t.Fatalf("record lost: %+v", records)
	}
	if _, err := s.Rollback(context.Background(), path, 0, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackScopeInvalidationAndEviction(t *testing.T) {
	m, s := rollbackTestSession(t)
	m.maxRecords, m.maxBytes = 2, 6
	dir := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	rollbackTestEdit(t, s, filepath.Join(dir, "a"), "new", false)
	m.SetCurrent("other", "session")
	other, _ := m.Session("other", "session")
	rollbackTestEdit(t, other, filepath.Join(dir, "b"), "new", false)
	result := rollbackTestEdit(t, s, filepath.Join(dir, "c"), "new", false)
	if result.RollbackEvicted != 1 {
		t.Fatalf("evicted: %d", result.RollbackEvicted)
	}
	if _, err := s.Rollback(context.Background(), filepath.Join(dir, "a"), 0, nil); !errors.Is(err, ErrRollbackNotFound) {
		t.Fatalf("oldest not evicted: %v", err)
	}
	if records, _ := other.List(); len(records) != 1 {
		t.Fatal("other scope lost")
	}
	m.SetCurrent("user", "session") // Same current session is not a switch.
	if records, _ := s.List(); len(records) != 1 {
		t.Fatal("same session cleared")
	}
	m.SetCurrent("user", "next")
	m.SetCurrent("user", "session")
	if _, err := s.List(); !errors.Is(err, ErrRollbackExpired) {
		t.Fatalf("old lease revived: %v", err)
	}
	current, _ := m.Session("user", "session")
	if records, _ := current.List(); len(records) != 0 {
		t.Fatal("records revived")
	}
	if records, _ := other.List(); len(records) != 1 {
		t.Fatal("other scope cleared")
	}
	text := "late"
	if _, err := s.EditFile(context.Background(), filepath.Join(dir, "late"), "", "", true, 3, []Edit{{Operation: "overwrite", NewText: &text}}, EditFileOptions{}, nil); !errors.Is(err, ErrRollbackExpired) {
		t.Fatalf("stale edit: %v", err)
	}
	m.SetCurrent("other", "")
	if m.bytes != 0 || len(m.records) != 0 {
		t.Fatalf("retained bytes=%d records=%d", m.bytes, len(m.records))
	}
}

func TestRollbackByteBudget(t *testing.T) {
	m, s := rollbackTestSession(t)
	m.maxBytes = 4
	dir := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	rollbackTestEdit(t, s, filepath.Join(dir, "a"), "new", false)
	result := rollbackTestEdit(t, s, filepath.Join(dir, "b"), "new", false)
	if result.RollbackEvicted != 1 || m.bytes != 3 {
		t.Fatalf("result=%+v bytes=%d", result, m.bytes)
	}
}

func TestRollbackSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary")
	}
	_, s := rollbackTestSession(t)
	dir := t.TempDir()
	path, link, other := filepath.Join(dir, "real"), filepath.Join(dir, "link"), filepath.Join(dir, "other")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	rollbackTestEdit(t, s, link, "after", false)
	if _, err := s.Rollback(context.Background(), link, 0, nil); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(link)
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("link replaced")
	}
	rollbackTestEdit(t, s, link, "after", false)
	if err := os.WriteFile(other, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rollback(context.Background(), link, 0, nil); err == nil {
		t.Fatal("retargeted link accepted")
	}
	if _, err := s.Rollback(context.Background(), path, 0, nil); err == nil {
		t.Fatal("original path retargeting ignored")
	}
}

func TestRollbackConcurrentCallsConsumeOnce(t *testing.T) {
	m, s := rollbackTestSession(t)
	path := filepath.Join(t.TempDir(), "file")
	rollbackTestEdit(t, s, path, "created", true)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Rollback(context.Background(), path, 0, nil); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("success=%d", success)
	}
	if len(m.locks) != 0 {
		t.Fatalf("leaked locks=%d", len(m.locks))
	}
}

func TestRollbackWriteFailuresKeepPreviousBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions differ on Windows")
	}
	_, s := rollbackTestSession(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	rollbackTestEdit(t, s, path, "edited", false)
	records, _ := s.List()
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if probe, err := os.CreateTemp(dir, "permission-probe"); err == nil {
		name := probe.Name()
		probe.Close()
		os.Remove(name)
		t.Skip("test process bypasses directory permissions")
	}
	text := "next"
	if _, err := s.EditFile(context.Background(), path, "", ContentRevision([]byte("edited")), false, 3,
		[]Edit{{Operation: "overwrite", NewText: &text}}, EditFileOptions{}, nil); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	if _, err := s.Rollback(context.Background(), path, 0, nil); err == nil {
		t.Fatal("rollback unexpectedly succeeded")
	}
	remaining, _ := s.List()
	if len(remaining) != 1 || remaining[0].ID != records[0].ID {
		t.Fatal("failed writes consumed backup")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rollback(context.Background(), path, 0, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Fatalf("restored %q", data)
	}
}

func TestRollbackLateWriteCannotRepopulateExpiredScope(t *testing.T) {
	m, s := rollbackTestSession(t)
	path := filepath.Join(t.TempDir(), "file")
	text := "late"
	result, err := s.EditFile(context.Background(), path, "", "", true, 3,
		[]Edit{{Operation: "overwrite", NewText: &text}}, EditFileOptions{}, func(string) error {
			m.SetCurrent("user", "next")
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if result.RollbackAvailable || len(m.records) != 0 || m.bytes != 0 {
		t.Fatal("late write republished backup")
	}
	m.SetCurrent("user", "session")
	current, _ := m.Session("user", "session")
	if records, _ := current.List(); len(records) != 0 {
		t.Fatal("backup revived after resume")
	}
}

func TestRollbackConcurrentEditsPreserveWinningSnapshot(t *testing.T) {
	_, s := rollbackTestSession(t)
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, text := range []string{"first", "second"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			_, err := s.EditFile(context.Background(), path, "", ContentRevision([]byte("original")), false, 3,
				[]Edit{{Operation: "overwrite", NewText: &text}}, EditFileOptions{}, nil)
			results <- err
		}(text)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful edits: %d", successes)
	}
	if _, err := s.Rollback(context.Background(), path, 0, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Fatalf("snapshot was not original: %q", data)
	}
}
func TestRollbackWaitCancellation(t *testing.T) {
	m, s := rollbackTestSession(t)
	path := filepath.Join(t.TempDir(), "file")
	rollbackTestEdit(t, s, path, "created", true)
	unlock, err := m.lockTarget(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Rollback(ctx, path, 0, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	unlock()
	if len(m.locks) != 0 {
		t.Fatal("leaked lock")
	}
}
