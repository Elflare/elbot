package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestReadServiceMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "elbot.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pid, err := readServiceMarker(path)
	if err != nil {
		t.Fatalf("readServiceMarker() error = %v", err)
	}
	if pid != os.Getpid() {
		t.Fatalf("pid = %d, want %d", pid, os.Getpid())
	}
}

func TestProcessAliveCurrentProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("service marker process liveness is disabled on windows")
	}
	if !processAlive(os.Getpid()) {
		t.Fatalf("current process should be alive")
	}
}
