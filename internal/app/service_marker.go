package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

type serviceMarker struct {
	path string
}

func serviceMarkerRunning() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	path := serviceMarkerPath()
	pid, err := readServiceMarker(path)
	if err != nil {
		return false
	}
	if processAlive(pid) {
		return true
	}
	_ = os.Remove(path)
	return false
}

func claimServiceMarker() (*serviceMarker, error) {
	if runtime.GOOS == "windows" {
		return &serviceMarker{}, nil
	}
	path := serviceMarkerPath()
	pid, err := readServiceMarker(path)
	if err == nil && processAlive(pid) {
		return nil, fmt.Errorf("elbot service already appears to be running with pid %d", pid)
	}
	if err == nil {
		_ = os.Remove(path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create service marker directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write service marker: %w", err)
	}
	return &serviceMarker{path: path}, nil
}

func (m *serviceMarker) Close() error {
	if m == nil || m.path == "" {
		return nil
	}
	pid, err := readServiceMarker(m.path)
	if err == nil && pid != os.Getpid() {
		return nil
	}
	return os.Remove(m.path)
}

func serviceMarkerPath() string {
	base := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	if base == "" {
		base = filepath.Join(os.TempDir(), fmt.Sprintf("elbot-%d", os.Getuid()))
	}
	return filepath.Join(base, "elbot", "elbot.pid")
}

func readServiceMarker(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid service marker pid")
	}
	return pid, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
