package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListDirectoriesReturnsOnlyChildDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zeta", "alpha"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ListDirectories(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != root || result.Parent != filepath.Dir(root) || !reflect.DeepEqual([]string{result.Directories[0].Name, result.Directories[1].Name}, []string{"alpha", "zeta"}) {
		t.Fatalf("listing = %+v", result)
	}
}

func TestListWorkspaceFilesListsCurrentAndNestedDirectories(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"zeta.txt": "z", "alpha.txt": "a"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(nested, "child.txt"), []byte("child"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	listing, err := ListWorkspaceFiles(t.Context(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"nested:directory", "alpha.txt:file", "zeta.txt:file"}
	got := make([]string, 0, len(listing.Items))
	for _, item := range listing.Items {
		got = append(got, item.Name+":"+item.Kind)
	}
	if listing.Path != "." || !reflect.DeepEqual(got, want) {
		t.Fatalf("listing = %+v", listing)
	}

	nestedListing, err := ListWorkspaceFiles(t.Context(), root, "nested")
	if err != nil || len(nestedListing.Items) != 1 || nestedListing.Items[0].Path != "nested/child.txt" {
		t.Fatalf("nested listing = %+v, %v", nestedListing, err)
	}
	if _, err := ListWorkspaceFiles(t.Context(), root, "../outside"); err == nil {
		t.Fatal("parent traversal accepted")
	}
	if _, err := ListWorkspaceFiles(t.Context(), root, "escape"); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestListDirectoriesRejectsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListDirectories(t.Context(), file); err == nil {
		t.Fatal("file accepted as directory")
	}
}
