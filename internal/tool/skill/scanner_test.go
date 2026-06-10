package skill

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/tool"
)

func TestFilesystemScannerScansPythonSkill(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "py", "docx"), "---\nname: docx\ndescription: DOCX skill\nrisk: low\n---\n\n# DOCX\n\nUse scripts.")
	scanner := NewFilesystemScanner(root)
	tools, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name() != "docx" || tools[0].Info().Source != tool.SourceSkillPy || tools[0].Info().Risk != tool.RiskLow {
		t.Fatalf("tools = %#v", tools)
	}
}

func TestFilesystemScannerScansGoSkillWithBinary(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "go", "foo")
	writeElyphSkill(t, dir, "#skill foo - Foo skill\n")
	binary := filepath.Join(dir, "foo")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if err := os.WriteFile(binary, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	scanner := NewFilesystemScanner(root)
	tools, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name() != "foo" || tools[0].Info().Source != tool.SourceSkillGo {
		t.Fatalf("tools = %#v", tools)
	}
}

func TestFilesystemScannerScansGoTextSkillWithoutBinary(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "go", "notes")
	writeElyphSkill(t, dir, "#skill notes - Notes workflow\n** risk low\n")
	scanner := NewFilesystemScanner(root)
	tools, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name() != "notes" || tools[0].Info().Source != tool.SourceSkillGo || tools[0].Info().Risk != tool.RiskLow {
		t.Fatalf("tools = %#v", tools)
	}
	detailer := tools[0].(DetailProvider)
	if len(detailer.ActivateTools()) != 0 {
		t.Fatalf("pure ELyph go skill should not activate runner: %#v", detailer.ActivateTools())
	}
}

func TestFilesystemScannerReloadRemovesDeletedExternalSkill(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "py", "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\n---\n\n# DOCX")
	scanner := NewFilesystemScanner(root)
	registry := tool.NewRegistry()
	if err := scanner.Reload(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("docx"); !ok {
		t.Fatal("docx should be registered")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := scanner.Reload(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("docx"); ok {
		t.Fatal("docx should be unregistered")
	}
}

func TestFilesystemScannerRemoveDeletesDirectoryAndReloads(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "py", "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\nrisk: low\n---\n\n# DOCX")
	scanner := NewFilesystemScanner(root)
	registry := tool.NewRegistry()
	if err := scanner.Reload(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	if err := scanner.Remove(context.Background(), registry, "docx"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("skill dir should be removed, err=%v", err)
	}
	if _, ok := registry.Get("docx"); ok {
		t.Fatal("docx should be unregistered after remove")
	}
}

func TestPythonDescriptorDetailAddsUvConstraint(t *testing.T) {
	d := NewDescriptor(Record{Name: "docx", Detail: "# DOCX", Kind: KindPython})
	if !strings.Contains(d.Detail(), "uv run python") || len(d.ActivateTools()) != 1 || d.ActivateTools()[0] != PythonRunnerName {
		t.Fatalf("detail=%q activate=%#v", d.Detail(), d.ActivateTools())
	}
}

func writeSkill(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeElyphSkill(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.elyph"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
