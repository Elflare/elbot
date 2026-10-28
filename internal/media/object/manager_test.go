package object

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/media/share"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil"
)

type testBackend struct {
	mu          sync.Mutex
	data        map[string][]byte
	puts, signs int
	put         func(context.Context, string, io.Reader, int64, string) (string, error)
	open        func(context.Context, string) (io.ReadCloser, error)
	remove      func(context.Context, string) error
	sign        func(context.Context, string, time.Duration) (string, error)
}

func (b *testBackend) Put(ctx context.Context, id string, reader io.Reader, size int64, mime string) (string, error) {
	if b.put != nil {
		return b.put(ctx, id, reader, size, mime)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.data == nil {
		b.data = map[string][]byte{}
	}
	b.data[id] = data
	b.puts++
	return id, nil
}
func (b *testBackend) Open(ctx context.Context, location string) (io.ReadCloser, error) {
	if b.open != nil {
		return b.open(ctx, location)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.data[location]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (b *testBackend) Remove(ctx context.Context, location string) error {
	if b.remove != nil {
		return b.remove(ctx, location)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.data, location)
	return nil
}
func (b *testBackend) PresignGet(ctx context.Context, location string, expiry time.Duration) (string, error) {
	if b.sign != nil {
		return b.sign(ctx, location, expiry)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.signs++
	return fmt.Sprintf("https://media.test/%s?version=%d", location, b.signs), nil
}

func newTestManager(t *testing.T) (*Manager, *sqlite.Store, *testBackend) {
	t.Helper()
	store := testutil.NewSQLiteStore(t)
	backend := &testBackend{}
	cfg := config.Default()
	m, err := NewManager(t.Context(), Options{
		Backend: "local", MaxImportBytes: 1 << 20, DownloadTimeout: time.Second,
		Media: cfg.Media, Now: storage.Now,
	}, Deps{Media: store.Media(), References: store.MediaReferences(), Local: backend})
	if err != nil {
		t.Fatal(err)
	}
	return m, store, backend
}

func importTestMedia(t *testing.T, m *Manager, content string) share.MediaInfo {
	t.Helper()
	item, err := m.ImportBytes(t.Context(), []byte(content), share.Input{Name: "input.txt"}, share.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

type testRepository struct {
	MediaRepository
	upsert  func(context.Context, *storage.Media) error
	get     func(context.Context, string) (*storage.Media, error)
	recover func(context.Context) error
}

func (r testRepository) Upsert(ctx context.Context, item *storage.Media) error {
	if r.upsert != nil {
		return r.upsert(ctx, item)
	}
	return r.MediaRepository.Upsert(ctx, item)
}
func (r testRepository) Get(ctx context.Context, id string) (*storage.Media, error) {
	if r.get != nil {
		return r.get(ctx, id)
	}
	return r.MediaRepository.Get(ctx, id)
}
func (r testRepository) RecoverInterrupted(ctx context.Context) error {
	if r.recover != nil {
		return r.recover(ctx)
	}
	return r.MediaRepository.RecoverInterrupted(ctx)
}

func TestImportReadExportAndDeduplication(t *testing.T) {
	m, store, backend := newTestManager(t)
	input := share.Input{Name: "https://example.test/a%252Fb.txt?token=secret", Source: share.Source{Platform: "test", URL: "https://user:secret@example.test/file?token=secret", FileID: "/private/path"}}
	item, err := m.ImportBytes(t.Context(), []byte("hello"), input, share.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != "a%2Fb.txt" || item.MIMEType != "text/plain" || item.Size != 5 || !share.ValidID(item.ID) {
		t.Fatalf("info = %#v", item)
	}
	record, err := store.Media().Get(t.Context(), item.ID)
	if err != nil || record.SourceURL != "" || record.SourceFileID != "" || record.SourcePlatform != "test" {
		t.Fatalf("record = %#v, %v", record, err)
	}
	again := importTestMedia(t, m, "hello")
	info, err := m.Info(t.Context(), item.ID)
	if err != nil || again != item || info != item || backend.puts != 1 {
		t.Fatalf("dedup/read changed metadata: %#v %#v %v", again, info, err)
	}
	data, got, err := m.Read(t.Context(), item.ID)
	if err != nil || string(data) != "hello" || got != item {
		t.Fatalf("Read = %q %#v %v", data, got, err)
	}
	destination := filepath.Join(t.TempDir(), "export")
	if _, err := m.Export(t.Context(), item.ID, destination); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(destination)
	if err != nil || string(data) != "hello" {
		t.Fatalf("export = %q %v", data, err)
	}
	if refs, err := store.MediaReferences().ListMediaIDs(t.Context(), item.ID); err != nil || len(refs) != 0 {
		t.Fatalf("leaked refs: %#v %v", refs, err)
	}
}

func TestImportLimitsAndRollback(t *testing.T) {
	m, _, backend := newTestManager(t)
	m.options.MaxImportBytes = 4
	for _, limits := range []share.ImportLimits{{}, {MaxImportBytes: 100}, {MaxImportBytes: 2}} {
		if _, err := m.ImportReader(t.Context(), strings.NewReader("12345"), -1, share.Input{}, limits); err == nil {
			t.Fatal("unbounded reader accepted")
		}
	}
	if backend.puts != 0 {
		t.Fatal("oversized content stored")
	}
	failure, rollback := errors.New("database failure"), errors.New("remove failure")
	ctx, cancel := context.WithCancel(t.Context())
	m.media = testRepository{MediaRepository: m.media, upsert: func(context.Context, *storage.Media) error { cancel(); return failure }}
	backend.remove = func(ctx context.Context, _ string) error {
		if ctx.Err() != nil {
			t.Error("rollback inherited canceled context")
		}
		return rollback
	}
	_, err := m.ImportBytes(ctx, []byte("ok"), share.Input{}, share.ImportLimits{})
	if !errors.Is(err, failure) || !errors.Is(err, rollback) {
		t.Fatalf("rollback errors = %v", err)
	}
}

func TestRecoveryAndPartialConstruction(t *testing.T) {
	m, store, _ := newTestManager(t)
	item := importTestMedia(t, m, "recover")
	held, err := m.Hold(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	ref := Reference{MediaID: item.ID, OwnerType: "test", OwnerID: "business", Purpose: "content"}
	if err := m.AddReference(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewManager(t.Context(), m.options, Deps{Media: m.media, References: m.references, Local: m.local})
	if err != nil || recovered == nil {
		t.Fatalf("recovery: %v", err)
	}
	refs, err := store.MediaReferences().ListMediaIDs(t.Context(), item.ID)
	if err != nil || len(refs) != 1 || refs[0].OwnerID != "business" {
		t.Fatalf("recovery refs = %#v %v", refs, err)
	}
	_ = held.Close(t.Context())
	failure := errors.New("recovery failed")
	partial, err := NewManager(t.Context(), m.options, Deps{Media: testRepository{recover: func(context.Context) error { return failure }}})
	if partial == nil || !errors.Is(err, failure) {
		t.Fatalf("partial = %v %v", partial, err)
	}
}

func TestConcurrentImportsDeduplicate(t *testing.T) {
	m, _, backend := newTestManager(t)
	var workers sync.WaitGroup
	for range 24 {
		workers.Go(func() {
			if _, err := m.ImportBytes(t.Context(), []byte("same content"), share.Input{}, share.ImportLimits{}); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	if backend.puts != 1 {
		t.Fatalf("puts = %d", backend.puts)
	}
	if len(m.objects.entries) != 0 {
		t.Fatal("object locks leaked")
	}
}
