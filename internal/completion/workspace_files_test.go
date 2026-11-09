package completion

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceFilesSourceCompletesWorkspacePaths(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "src", "test.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package test"), 0o600); err != nil {
		t.Fatal(err)
	}

	source := WorkspaceFilesSource{Root: func(context.Context) (string, error) { return root, nil }}
	items, err := source.Complete(t.Context(), Request{Text: "look #tes", Cursor: len("look #tes")})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Text != "#src/test.go" || items[0].Label != "src/test.go" {
		t.Fatalf("items = %#v", items)
	}
}
