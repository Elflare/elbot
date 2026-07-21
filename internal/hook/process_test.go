package hook

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewProcessEnvironmentMergesDotEnvAndAppendsPath(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	third := filepath.Join(t.TempDir(), "third")
	basePath := strings.Join([]string{first, second}, string(os.PathListSeparator))
	extraPath := strings.Join([]string{second, third}, string(os.PathListSeparator))
	environment := NewProcessEnvironment(
		[]string{"PATH=" + basePath, "TOKEN=from-process", "EMPTY="},
		map[string]string{"PATH": extraPath, "TOKEN": "from-dotenv", "EXTRA": "available", "EMPTY": "from-dotenv"},
	)

	values := environmentMap(environment.environ)
	if values[environmentKey("TOKEN")] != "from-process" || values[environmentKey("EMPTY")] != "" || values[environmentKey("EXTRA")] != "available" {
		t.Fatalf("environment = %#v", values)
	}
	wantPath := strings.Join([]string{first, second, third}, string(os.PathListSeparator))
	if values[environmentKey("PATH")] != wantPath || environment.path != wantPath {
		t.Fatalf("PATH = %q, want %q", environment.path, wantPath)
	}
}

func TestProcessEnvironmentFindsExecutableInDotEnvPath(t *testing.T) {
	source, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()
	name := "elbot-hook-process-env-helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(dir, name)
	copyExecutable(t, source, target)

	environment := NewProcessEnvironment(os.Environ(), map[string]string{
		"PATH":                          dir,
		"ELBOT_HOOK_PROCESS_ENV_HELPER": "from-dotenv",
	})
	cmd := environment.Command(name, "-test.run=^TestProcessEnvironmentHelper$", "--")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run helper: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "from-dotenv" {
		t.Fatalf("helper environment = %q", got)
	}
}

func TestProcessEnvironmentHelper(t *testing.T) {
	value := os.Getenv("ELBOT_HOOK_PROCESS_ENV_HELPER")
	if value == "" {
		return
	}
	fmt.Fprint(os.Stdout, value)
	os.Exit(0)
}

func environmentMap(environ []string) map[string]string {
	values := make(map[string]string, len(environ))
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[environmentKey(name)] = value
		}
	}
	return values
}

func copyExecutable(t *testing.T, source, target string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatalf("open helper source: %v", err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatalf("create helper: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatalf("copy helper: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close helper: %v", err)
	}
}
