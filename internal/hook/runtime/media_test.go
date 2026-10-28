// 验证插件媒体 RPC、租约和文件访问约束。

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

	"elbot/internal/config"
	"elbot/internal/hook/share"
	"elbot/internal/media"
	"elbot/internal/storage/sqlite"
	"elbot/internal/testutil/mediatest"
)

func testMediaRuntime(t *testing.T, configure ...func(*config.Config)) (*Manager, *media.MediaCenter, string, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	root := t.TempDir()
	center := mediatest.New(t, store, root, configure...)
	manager, err := NewManager(Options{Log: testLog, Notify: testNotify, SharedDir: t.TempDir(), Media: center})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return manager, center, t.TempDir(), store
}

func mediaCall(t *testing.T, m *Manager, dir, method string, params any) mediaResult {
	t.Helper()
	value, err := m.MediaRequest(context.Background(), dir, method, mustJSON(params))
	if err != nil {
		t.Fatal(err)
	}
	result := value.(mediaResult)
	return result
}

func TestMediaProtocolImportReadExportAndLeases(t *testing.T) {
	m, center, dir, store := testMediaRuntime(t)
	ctx := context.Background()
	small := []byte{0, 255, 128, 1}
	imported := mediaCall(t, m, dir, "media.import", map[string]any{"base64": base64.StdEncoding.EncodeToString(small), "name": "tiny.png", "mime_type": "image/png"})
	read := mediaCall(t, m, dir, "media.read", map[string]string{"media": imported.MediaID})
	if read.Base64 != base64.StdEncoding.EncodeToString(small) || read.Path != "" {
		t.Fatalf("read=%#v", read)
	}
	refs, err := store.MediaReferences().ListByOwner(ctx, "hook", m.media.owner)
	if err != nil || len(refs) != 1 {
		t.Fatalf("refs=%#v %v", refs, err)
	}
	large := bytes.Repeat([]byte("x"), mediaInlineBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "large.bin"), large, 0600); err != nil {
		t.Fatal(err)
	}
	importedLarge := mediaCall(t, m, dir, "media.import", map[string]string{"path": "large.bin"})
	exported := mediaCall(t, m, dir, "media.read", map[string]string{"media": importedLarge.MediaID})
	if exported.Base64 != "" || exported.Path == "" || !strings.HasPrefix(filepath.Base(filepath.Dir(exported.Path)), "elbot-hook-media-") {
		t.Fatalf("export=%#v", exported)
	}
	data, err := os.ReadFile(exported.Path)
	if err != nil || !bytes.Equal(data, large) {
		t.Fatalf("export bytes: %v", err)
	}
	repeated := mediaCall(t, m, dir, "media.export", map[string]string{"media": importedLarge.MediaID})
	if repeated.Path != exported.Path {
		t.Fatal("export should reuse the lease")
	}
	// Releasing a temporary Hook owner must not release a transcript owner.
	ref := media.Reference{MediaID: importedLarge.MediaID, OwnerType: "tool_result", OwnerID: "transcript", Purpose: "content"}
	if err := center.AddReference(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := m.media.prune(ctx, time.Now().Add(2*mediaLeaseTTL), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(exported.Path); !os.IsNotExist(err) {
		t.Fatalf("export survived expiry: %v", err)
	}
	refs, err = store.MediaReferences().ListByOwner(ctx, "hook", m.media.owner)
	if err != nil || len(refs) != 0 {
		t.Fatalf("expired refs=%#v %v", refs, err)
	}
	refs, err = store.MediaReferences().ListByOwner(ctx, "tool_result", "transcript")
	if err != nil || len(refs) != 1 {
		t.Fatalf("transcript refs=%#v %v", refs, err)
	}
	// Close also releases files and references, without deleting media bodies.
	exported = mediaCall(t, m, dir, "media.export", map[string]string{"media": imported.MediaID})
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(exported.Path); !os.IsNotExist(err) {
		t.Fatalf("export survived close: %v", err)
	}
	if _, _, err := center.Read(ctx, imported.MediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MediaRequest(ctx, dir, "media.metadata", mustJSON(map[string]string{"media": imported.MediaID})); err == nil {
		t.Fatal("closed runtime accepted request")
	}
}

func TestMediaProtocolURLAndInvalidInputs(t *testing.T) {
	m, _, dir, store := testMediaRuntime(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("url media")) }))
	defer server.Close()
	imported := mediaCall(t, m, dir, "media.import", map[string]string{"url": server.URL + "/image?X-Amz-Signature=secret"})
	metadata, err := store.Media().Get(context.Background(), imported.MediaID)
	if err != nil || metadata.SourceURL != "" {
		t.Fatalf("persisted signed URL: %#v %v", metadata, err)
	}
	tests := []struct{ method, params string }{
		{"media.import", `{}`}, {"media.import", `{"base64":"!"}`},
		{"media.import", `{"base64":"YQ==","url":"https://example.com"}`},
		{"media.import", `{"path":"../secret"}`}, {"media.import", `{"path":"."}`},
		{"media.read", `{"media":"media:bad"}`}, {"media.metadata", `{"media":"media:bad"}`},
		{"media.export", `{"media":"media:bad","path":"../../escape"}`},
		{"media.delete", `{}`}, {"media.import", `{"base64":"YQ==","object_key":"secret"}`},
	}
	for _, test := range tests {
		if _, err := m.MediaRequest(context.Background(), dir, test.method, json.RawMessage(test.params)); err == nil {
			t.Fatalf("accepted %s %s", test.method, test.params)
		}
	}
	if _, err := m.MediaRequest(context.Background(), dir, "media.import", mustJSON(map[string]string{"path": filepath.Join(t.TempDir(), "secret")})); err == nil {
		t.Fatal("accepted absolute path")
	}
	if _, err := m.MediaRequest(context.Background(), dir, "media.import", mustJSON(map[string]string{"base64": strings.Repeat("A", 14*1024*1024)})); err == nil {
		t.Fatal("accepted oversized base64")
	}
	m, _, dir, _ = testMediaRuntime(t, func(cfg *config.Config) { cfg.PlatformFiles.MaxReceiveFileBytes = 2 })
	if err := os.WriteFile(filepath.Join(dir, "oversized"), []byte("large"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MediaRequest(context.Background(), dir, "media.import", mustJSON(map[string]string{"path": "oversized"})); err == nil {
		t.Fatal("bypassed center import limit")
	}
}

func TestMediaProtocolRejectsSymlinkEscape(t *testing.T) {
	m, _, dir, _ := testMediaRuntime(t)
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
	m, _, dir, _ := testMediaRuntime(t)
	cfg := share.RuntimeConfig{ID: "media", Dir: dir, Mode: share.RuntimeModePersistent, Command: runtimeHelperCommand(), Cwd: ".", StartupTimeoutSeconds: 5, ShutdownTimeoutSeconds: 2, EventTimeoutSeconds: 5, MaxWaitSeconds: 30, Restart: share.RuntimeRestartConfig{Strategy: "never", InitialDelaySeconds: 1, MaxDelaySeconds: 1}}
	if err := m.Apply([]share.RuntimeConfig{cfg}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, m, "media", StatusReady)
	event, _, err := m.Handle(context.Background(), "media", share.Event{Actor: share.ActorContext{ID: "test:media"}}, share.Control{})
	if err != nil {
		t.Fatal(err)
	}
	if len(event.Message.Segments) != 1 || !media.ValidID(event.Message.Segments[0].MediaID) || len(event.Outputs) != 1 || event.Outputs[0].Source.MediaID != event.Message.Segments[0].MediaID {
		t.Fatalf("event=%#v", event)
	}
}
func TestWorkerMediaDispatch(t *testing.T) {

	m, _, dir, _ := testMediaRuntime(t)
	w := newWorker(m, share.RuntimeConfig{Dir: dir, EventTimeoutSeconds: 5})
	value, err := w.pluginRequest(frame{Method: "media.import", Params: mustJSON(map[string]string{"base64": "YQ=="})})
	if err != nil || !media.ValidID(value.(mediaResult).MediaID) {
		t.Fatalf("dispatch: %#v %v", value, err)
	}
}
