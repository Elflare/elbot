package workspace

import (
	"elbot/internal/operationresult"
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
	if err != nil || result.Status != operationresult.Success {
		t.Fatalf("directories: %+v %v", result, err)
	}
	if result.Data.Path != root || result.Data.Parent != filepath.Dir(root) || !reflect.DeepEqual([]string{result.Data.Directories[0].Name, result.Data.Directories[1].Name}, []string{"alpha", "zeta"}) {
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
	if err != nil || listing.Status != operationresult.Success {
		t.Fatalf("listing error = %v, result = %+v", err, listing)
	}
	want := []string{"nested:directory", "alpha.txt:file", "zeta.txt:file"}
	got := make([]string, 0, len(listing.Data.Items))
	for _, item := range listing.Data.Items {
		got = append(got, item.Name+":"+item.Kind)
	}
	if listing.Data.Path != "." || !reflect.DeepEqual(got, want) {
		t.Fatalf("listing = %+v", listing)
	}

	nestedListing, err := ListWorkspaceFiles(t.Context(), root, "nested")
	if err != nil || nestedListing.Status != operationresult.Success || len(nestedListing.Data.Items) != 1 || nestedListing.Data.Items[0].Path != "nested/child.txt" {
		t.Fatalf("nested listing = %+v, %v", nestedListing, err)
	}
	traversal, err := ListWorkspaceFiles(t.Context(), root, "../outside")
	if err != nil || traversal.Status != operationresult.InvalidArguments {
		t.Fatal("parent traversal accepted")
	}
	missing, err := ListWorkspaceFiles(t.Context(), root, "missing")
	if err != nil || missing.Status != operationresult.NotFound {
		t.Fatalf("missing directory result = %+v, err = %v", missing, err)
	}
	file, err := ListWorkspaceFiles(t.Context(), root, "alpha.txt")
	if err != nil || file.Status != operationresult.InvalidTarget {
		t.Fatalf("file path result = %+v, err = %v", file, err)
	}
	escape, err := ListWorkspaceFiles(t.Context(), root, "escape")
	if err == nil && escape.Status == operationresult.Success {
		t.Fatal("symlink escape accepted")
	}
	unavailable, err := ListWorkspaceFiles(t.Context(), "", ".")
	if err != nil || unavailable.Status != operationresult.Unavailable {
		t.Fatalf("unconfigured workspace result = %+v, err = %v", unavailable, err)
	}
}

func TestListDirectoriesRejectsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := ListDirectories(t.Context(), file); err != nil || result.Status != operationresult.InvalidTarget {
		t.Fatalf("file as directory: %+v %v", result, err)
	}
}
