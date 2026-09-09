package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/hook"
	"elbot/internal/media"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func testMediaRuntime(t *testing.T) (*Manager, *media.Manager, string) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	manager := NewManager(Options{Media: center})
	t.Cleanup(func() {
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return manager, center, t.TempDir()
}

func mediaCall(t *testing.T, m *Manager, dir, method string, params any) mediaResult {
	t.Helper()
	value, err := m.MediaRequest(context.Background(), dir, method, mustJSON(params))
	if err != nil {
		t.Fatal(err)
	}
	result := value.(mediaResult)
	raw := string(mustJSON(result))
	for _, forbidden := range []string{"LocalPath", "ObjectKey", "SourceURL", "Backend", "local_path", "object_key", "X-Amz-"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("leaked metadata: %s", raw)
		}
	}
	return result
}

func TestMediaProtocolImportReadExportAndLeases(t *testing.T) {
	m, center, dir := testMediaRuntime(t)
	ctx := context.Background()
	small := []byte{0, 255, 128, 1}
	imported := mediaCall(t, m, dir, "media.import", map[string]any{"base64": base64.StdEncoding.EncodeToString(small), "name": "tiny.png", "mime_type": "image/png"})
	read := mediaCall(t, m, dir, "media.read", map[string]string{"media_id": imported.MediaID})
	if read.Base64 != base64.StdEncoding.EncodeToString(small) || read.Path != "" {
		t.Fatalf("read=%#v", read)
	}
	refs, err := center.Store.MediaReferences().ListByOwner(ctx, "hook", m.media.owner)
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs=%#v %v", refs, err)
	}
	large := bytes.Repeat([]byte("x"), mediaInlineBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "large.bin"), large, 0600); err != nil {
		t.Fatal(err)
	}
	importedLarge := mediaCall(t, m, dir, "media.import", map[string]string{"path": "large.bin"})
	exported := mediaCall(t, m, dir, "media.read", map[string]string{"media_id": importedLarge.MediaID})
	if exported.Base64 != "" || exported.Path == "" || strings.HasPrefix(exported.Path, center.Root) {
		t.Fatalf("export=%#v", exported)
	}
	data, err := os.ReadFile(exported.Path)
	if err != nil || !bytes.Equal(data, large) {
		t.Fatalf("export bytes: %v", err)
	}
	repeated := mediaCall(t, m, dir, "media.export", map[string]string{"media_id": importedLarge.MediaID})
	if repeated.Path != exported.Path {
		t.Fatal("export should reuse the lease")
	}
	// Releasing a temporary Hook owner must not release a transcript owner.
	ref := &storage.MediaReference{MediaID: importedLarge.MediaID, OwnerType: "tool_result", OwnerID: "transcript", Purpose: "content"}
	if err := center.AddReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := m.media.prune(ctx, time.Now().Add(2*mediaLeaseTTL), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(exported.Path); !os.IsNotExist(err) {
		t.Fatalf("export survived expiry: %v", err)
	}
	refs, err = center.Store.MediaReferences().ListByOwner(ctx, "hook", m.media.owner)
	if err != nil || len(refs) != 0 {
		t.Fatalf("expired refs=%#v %v", refs, err)
	}
	refs, err = center.Store.MediaReferences().ListByOwner(ctx, "tool_result", "transcript")
	if err != nil || len(refs) != 1 {
		t.Fatalf("transcript refs=%#v %v", refs, err)
	}
	// Close also releases files and references, without deleting media bodies.
	exported = mediaCall(t, m, dir, "media.export", map[string]string{"media_id": imported.MediaID})
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(exported.Path); !os.IsNotExist(err) {
		t.Fatalf("export survived close: %v", err)
	}
	if _, _, err := center.Read(ctx, imported.MediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MediaRequest(ctx, dir, "media.metadata", mustJSON(map[string]string{"media_id": imported.MediaID})); err == nil {
		t.Fatal("closed runtime accepted request")
	}
}

func TestMediaProtocolURLAndInvalidInputs(t *testing.T) {
	m, center, dir := testMediaRuntime(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("url media")) }))
	defer server.Close()
	imported := mediaCall(t, m, dir, "media.import", map[string]string{"url": server.URL + "/image?X-Amz-Signature=secret"})
	metadata, err := center.Metadata(context.Background(), imported.MediaID)
	if err != nil || metadata.SourceURL != "" {
		t.Fatalf("persisted signed URL: %#v %v", metadata, err)
	}
	tests := []struct{ method, params string }{
		{"media.import", `{}`}, {"media.import", `{"base64":"!"}`},
		{"media.import", `{"base64":"YQ==","url":"https://example.com"}`},
		{"media.import", `{"path":"../secret"}`}, {"media.import", `{"path":"."}`},
		{"media.read", `{"media_id":"media:bad"}`}, {"media.metadata", `{"media_id":"media:bad"}`},
		{"media.export", `{"media_id":"media:bad","path":"../../escape"}`},
		{"media.delete", `{}`}, {"media.import", `{"base64":"YQ==","object_key":"secret"}`},
	}
	for _, test := range tests {
		if _, err := m.MediaRequest(context.Background(), dir, test.method, json.RawMessage(test.params)); err == nil {
			t.Fatalf("accepted %s %s", test.method, test.params)
		}
	}
	if _, err := m.MediaRequest(context.Background(), dir, "media.import", mustJSON(map[string]string{"path": filepath.Join(center.Root, "secret")})); err == nil {
		t.Fatal("accepted absolute path")
	}
	if _, err := m.MediaRequest(context.Background(), dir, "media.import", mustJSON(map[string]string{"base64": strings.Repeat("A", 14*1024*1024)})); err == nil {
		t.Fatal("accepted oversized base64")
	}
	center.MaxImportBytes = 2
	if err := os.WriteFile(filepath.Join(dir, "oversized"), []byte("large"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MediaRequest(context.Background(), dir, "media.import", mustJSON(map[string]string{"path": "oversized"})); err == nil {
		t.Fatal("bypassed center import limit")
	}
}

func TestMediaProtocolRejectsSymlinkEscape(t *testing.T) {
	m, _, dir := testMediaRuntime(t)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := m.MediaRequest(context.Background(), dir, "media.import", mustJSON(map[string]string{"path": "escape"})); err == nil {
		t.Fatal("accepted escaping symlink")
	}
}

func TestWorkerMediaProtocolRoundTrip(t *testing.T) {
	m, _, dir := testMediaRuntime(t)
	cfg := Config{ID: "media", Dir: dir, Mode: ModePersistent, Command: runtimeHelperCommand(), Cwd: ".", StartupTimeoutSeconds: 5, ShutdownTimeoutSeconds: 2, EventTimeoutSeconds: 5, MaxWaitSeconds: 30, Restart: RestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1}}
	if err := m.Apply([]Config{cfg}); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, m, "media", StatusReady)
	event, err := m.Handle(context.Background(), "media", hook.Event{Actor: hook.ActorContext{ID: "test:media"}}, hook.Control{})
	if err != nil {
		t.Fatal(err)
	}
	if len(event.Message.Segments) != 1 || !media.ValidID(event.Message.Segments[0].MediaID) || len(event.Outputs) != 1 || event.Outputs[0].Source.MediaID != event.Message.Segments[0].MediaID {
		t.Fatalf("event=%#v", event)
	}
}
func TestWorkerMediaDispatch(t *testing.T) {

	m, _, dir := testMediaRuntime(t)
	w := newWorker(m, Config{Dir: dir, EventTimeoutSeconds: 5})
	value, err := w.pluginRequest(frame{Method: "media.import", Params: mustJSON(map[string]string{"base64": "YQ=="})})
	if err != nil || !media.ValidID(value.(mediaResult).MediaID) {
		t.Fatalf("dispatch: %#v %v", value, err)
	}
}
