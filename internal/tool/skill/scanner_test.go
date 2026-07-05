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

func TestFilesystemScannerScansAgentSkill(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "agent", "docx"), "---\nname: docx\ndescription: DOCX skill\nrisk: low\n---\n\n# DOCX\n\nUse scripts.")
	scanner := NewFilesystemScanner(root)
	tools, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name() != "docx" || tools[0].Info().Source != tool.SourceSkillAgent || tools[0].Info().Risk != tool.RiskSafe {
		t.Fatalf("tools = %#v", tools)
	}
}

func TestFilesystemScannerScansCallableAgentSkill(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\nrisk: low\n---\n\n# DOCX")
	writeAgentSkillConfig(t, dir, `risk = "medium"
command = ["python", "foo.py"]
parameters = '''{"type":"object","required":["input"],"properties":{"input":{"type":"string"}}}'''
[args]
input = "--input"
`)
	scanner := NewFilesystemScanner(root)
	tools, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name() != "docx" || tools[0].Info().Risk != tool.RiskMedium {
		t.Fatalf("tools = %#v", tools)
	}
	if _, ok := tools[0].(CommandTool); !ok {
		t.Fatalf("tool type = %T, want CommandTool", tools[0])
	}
}

func TestFilesystemScannerKeepsInvalidManifestAsDocumentSkill(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\nrisk: low\n---\n\n# DOCX")
	writeAgentSkillConfig(t, dir, `risk = "nope"
command = ["python", "foo.py"]
parameters = '''{"type":"object","properties":{"input":{"type":"string"}}}'''
[args]
input = "--input"
`)
	scanner := NewFilesystemScanner(root)
	tools, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name() != "docx" || tools[0].Info().Risk != tool.RiskSafe {
		t.Fatalf("tools = %#v", tools)
	}
	if _, ok := tools[0].(Descriptor); !ok {
		t.Fatalf("tool type = %T, want Descriptor", tools[0])
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
	dir := filepath.Join(root, "agent", "docx")
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
	dir := filepath.Join(root, "agent", "docx")
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

func TestAgentDescriptorDetailAddsAgentSkillNotice(t *testing.T) {
	d := NewDescriptor(Record{Name: "docx", Detail: "# DOCX", Kind: KindAgent})
	if !strings.Contains(d.Detail(), "agent_skill_creator") || len(d.ActivateTools()) != 1 || d.ActivateTools()[0] != AgentSkillManagerName {
		t.Fatalf("detail=%q activate=%#v", d.Detail(), d.ActivateTools())
	}
}

func TestFilesystemScannerSkipsBrokenElyphWithoutFailing(t *testing.T) {
	root := t.TempDir()
	brokenDir := filepath.Join(root, "go", "broken")
	goodDir := filepath.Join(root, "go", "good")
	writeElyphSkill(t, brokenDir, "missing header\n> do\n")
	writeElyphSkill(t, goodDir, "#skill good - Good skill\n** risk low\n")
	scanner := NewFilesystemScanner(root)
	tools, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(tools) != 1 || tools[0].Name() != "good" {
		t.Fatalf("tools = %#v, want only good", tools)
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

func writeAgentSkillConfig(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, AgentSkillConfigFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
