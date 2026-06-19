package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/elyph"
)

func TestResolveGoExecutableReadsConfigEnv(t *testing.T) {
	configDir := t.TempDir()
	skillRoot := filepath.Join(configDir, "skills", "go", "resolver")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeGo := filepath.Join(configDir, "fake-go")
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, ".env"), []byte("ELBOT_GO_BINARY="+fakeGo+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(goBinaryEnv, "")

	path, err := resolveGoExecutable(skillRoot)
	if err != nil {
		t.Fatal(err)
	}
	if path != fakeGo {
		t.Fatalf("go path = %q, want %q", path, fakeGo)
	}
}

func TestResolveGoExecutableUsesGOROOT(t *testing.T) {
	root := t.TempDir()
	goBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(goBin, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeGo := filepath.Join(goBin, executableName("go"))
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(goBinaryEnv, "")
	t.Setenv("GOROOT", root)

	path, err := resolveGoExecutable(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if path != fakeGo {
		t.Fatalf("go path = %q, want %q", path, fakeGo)
	}
}

func TestResolveGoExecutableReportsInvalidConfiguredPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-go")
	t.Setenv(goBinaryEnv, missing)

	_, err := resolveGoExecutable(t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
	if text := err.Error(); !strings.Contains(text, goBinaryEnv) || !strings.Contains(text, missing) {
		t.Fatalf("err = %v", err)
	}
}

func writeTestGoSkill(t *testing.T, root, name, source string) {
	t.Helper()
	dir := filepath.Join(root, "go", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillText := "#skill " + name + " - Test.\n** risk low\n<- $payload:object!\n-> $result:str\n"
	if _, err := elyph.ParseSkill(skillText, name); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, elyph.SkillFileName), []byte(skillText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module elbot-skill/"+name+"\n\ngo 1.20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestGoSource(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go", name, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func readTestGoSkillElyph(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go", name, elyph.SkillFileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
