package media

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestImportSanitizesMediaMetadataAndReturnedCopies(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	manager := NewManager(store, root, &LocalBackend{Root: root})

	cases := []struct {
		name       string
		input      Input
		wantName   string
		wantURL    string
		wantFileID string
	}{
		{
			name: "credential URL",
			input: Input{
				Name: "https://user:password@example.com/folder/photo.png?rkey=name-secret#part",
				Source: Source{
					URL:    "https://user:password@example.com/folder/photo.png?rkey=url-secret#part",
					FileID: "https://example.com/photo.png?rkey=id-secret",
				},
			},
			wantName: "photo.png",
		},
		{
			name: "signed URL",
			input: Input{
				Name: "https://example.com/folder/report.pdf?X-Amz-Signature=name-secret",
				Source: Source{
					URL:    "https://example.com/folder/report.pdf?X-Amz-Signature=url-secret",
					FileID: `C:\downloads\report.pdf`,
				},
			},
			wantName: "report.pdf",
		},
		{
			name: "telegram token path",
			input: Input{
				Name: `C:\private\image.jpg`,
				Source: Source{
					URL:    "https://api.telegram.org/file/bot123456:token/photos/image.jpg",
					FileID: "opaque-platform-id",
				},
			},
			wantName:   "image.jpg",
			wantFileID: "opaque-platform-id",
		},
		{
			name: "safe source",
			input: Input{
				Name: "/tmp/archive.bin",
				Source: Source{
					URL:    "https://cdn.example.com/files/archive.bin",
					FileID: "opaque_123-ABC",
				},
			},
			wantName:   "archive.bin",
			wantURL:    "https://cdn.example.com/files/archive.bin",
			wantFileID: "opaque_123-ABC",
		},
		{
			name:     "inline name",
			input:    Input{Name: "data:image/png;base64,c2VjcmV0"},
			wantName: "file",
		},
		{
			name:     "base64 name",
			input:    Input{Name: "base64:c2VjcmV0"},
			wantName: "file",
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item, err := manager.ImportBytes(ctx, []byte{byte(i), 1, 2, 3}, tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if item.Name != tc.wantName || item.SourceURL != tc.wantURL || item.SourceFileID != tc.wantFileID {
				t.Fatalf("metadata = %#v", item)
			}
			stored, err := store.Media().Get(ctx, item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Name != tc.wantName || stored.SourceURL != tc.wantURL || stored.SourceFileID != tc.wantFileID {
				t.Fatalf("stored metadata = %#v", stored)
			}
		})
	}

	legacy, err := manager.ImportBytes(ctx, []byte("legacy"), Input{Name: "safe.png"})
	if err != nil {
		t.Fatal(err)
	}
	legacy.Name = "https://example.com/private/legacy.png?rkey=legacy-name-secret"
	legacy.SourceURL = "https://user:pass@example.com/private/legacy.png#secret"
	legacy.SourceFileID = "https://example.com/private/legacy.png?rkey=legacy-id-secret"
	if err := store.Media().Upsert(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	deduplicated, err := manager.ImportBytes(ctx, []byte("legacy"), Input{Name: "ignored.png"})
	if err != nil {
		t.Fatal(err)
	}
	if deduplicated.Name != "legacy.png" || deduplicated.SourceURL != "" || deduplicated.SourceFileID != "" {
		t.Fatalf("deduplicated metadata = %#v", deduplicated)
	}
	metadata, err := manager.Metadata(ctx, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Name != "legacy.png" || metadata.SourceURL != "" || metadata.SourceFileID != "" {
		t.Fatalf("returned metadata = %#v", metadata)
	}
	stored, err := store.Media().Get(ctx, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.Name+stored.SourceURL+stored.SourceFileID, "secret") {
		t.Fatalf("legacy record was unexpectedly rewritten: %#v", stored)
	}
}
