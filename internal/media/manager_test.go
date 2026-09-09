package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"elbot/internal/storage/sqlite"
)

func TestValidID(t *testing.T) {
	valid := IDPrefix + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, id := range []string{valid, "", "media:short", "media:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg"} {
		want := id == valid
		if got := ValidID(id); got != want {
			t.Fatalf("ValidID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestLocalManagerImportReadExportAndDeduplicatesBinary(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()
	root := filepath.Join(t.TempDir(), "media")
	manager := NewManager(store, root, &LocalBackend{Root: root})
	data := []byte{0, 0xff, 0x80, 0x01, 0x7f}
	first, err := manager.ImportBytes(ctx, data, Input{Name: "sample.bin"})
	if err != nil {
		t.Fatalf("import bytes: %v", err)
	}
	if !ValidID(first.ID) || first.Size != int64(len(data)) || first.MIMEType != "application/octet-stream" {
		t.Fatalf("media metadata = %#v", first)
	}
	second, err := manager.ImportBytes(ctx, data, Input{Name: "other.bin"})
	if err != nil {
		t.Fatalf("deduplicated import: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("deduplicated IDs = %q and %q", first.ID, second.ID)
	}
	got, _, err := manager.Read(ctx, first.ID)
	if err != nil {
		t.Fatalf("read media: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("read bytes = %v, want %v", got, data)
	}
	exportPath := filepath.Join(t.TempDir(), "export.bin")
	if _, err := manager.Export(ctx, first.ID, exportPath); err != nil {
		t.Fatalf("export media: %v", err)
	}
	exported, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if string(exported) != string(data) {
		t.Fatalf("exported bytes = %v, want %v", exported, data)
	}
}
