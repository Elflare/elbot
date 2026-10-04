package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveStateReplacesWholeFileAndPreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	if err := os.WriteFile(path, []byte("old state"), 0o600); err != nil {
		t.Fatal(err)
	}
	var original *os.File
	if runtime.GOOS != "windows" {
		// Windows can deny replacement while a reader holds the destination.
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		original = file
		defer original.Close()
	}
	state := StateConfig{Session: StateSessionConfig{DefaultMode: "chat"}, ModeModels: map[string]ModelSelection{"work": {Provider: "p", Model: "new"}}}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ModeModels["work"].Model != "new" || loaded.Session.DefaultMode != "chat" {
		t.Fatalf("state = %#v", loaded)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %v", info.Mode())
	}
	// An open handle still sees the previous inode: publication used replacement,
	// not truncation of the active file.
	if original != nil {
		data := make([]byte, len("old state"))
		if _, err := original.ReadAt(data, 0); err != nil || string(data) != "old state" {
			t.Fatalf("old handle = %q, %v", data, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %#v", entries)
	}
}

func TestSaveStateThroughSymlinkReplacesTarget(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target.toml"), filepath.Join(dir, "state.toml")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.toml", link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := SaveState(link, StateConfig{ModeModels: map[string]ModelSelection{"work": {Provider: "p", Model: "new"}}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("state symlink was replaced")
	}
	loaded, err := LoadState(target)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ModeModels["work"].Model != "new" {
		t.Fatalf("target state = %#v", loaded)
	}
}

func TestSaveStateFailurePreservesInvalidDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "keep")
	if err := os.WriteFile(marker, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(path, StateConfig{}); err == nil {
		t.Fatal("expected invalid destination error")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "original" {
		t.Fatalf("destination changed: %q, %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %#v", entries)
	}
}
