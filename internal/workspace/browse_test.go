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

func TestListDirectoriesRejectsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListDirectories(t.Context(), file); err == nil {
		t.Fatal("file accepted as directory")
	}
}
