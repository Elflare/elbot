package toolrun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/security"
	"elbot/internal/storage/sqlite"
)

func TestExternalElwispDoesNotResolveHostMedia(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	center := media.NewManager(store, root, &media.LocalBackend{Root: root})
	item, err := center.ImportBytes(ctx, []byte("private host content"), media.Input{})
	if err != nil {
		t.Fatal(err)
	}
	var received elwispToolRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"content":"ok"}`))
	}))
	defer server.Close()
	manager := NewManager(nil, nil)
	manager.Media = center
	args, _ := json.Marshal(map[string]any{"source": item.ID, "media_inputs": []map[string]string{{"media": item.ID}}})
	call := llm.ToolCallRequest{ID: "call", Name: "external", Arguments: string(args)}
	resolved := ResolvedTool{Available: true, Source: SourceKindELwisp, Cached: &CachedTool{Name: "external", Endpoint: server.URL}}
	result := manager.Execute(ctx, call, resolved, security.Actor{Role: security.RoleSuperadmin})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if string(received.Arguments) != string(args) {
		t.Fatalf("host media leaked into external args: %s", received.Arguments)
	}
	refs, err := store.MediaReferences().ListMediaIDs(ctx, item.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("external acquired host media %v %v", refs, err)
	}
}
