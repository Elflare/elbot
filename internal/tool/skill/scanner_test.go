package skill

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"strings"
	"testing"

	toolshare "elbot/internal/tool/share"
)

func TestFilesystemScannerScansAgentSkill(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "docx"), "---\nname: docx\ndescription: DOCX skill\nrisk: low\n---\n\n# DOCX\n\nUse scripts.")
	writeSkill(t, filepath.Join(root, "nested", "ignored"), "Not a direct child")
	scanner := (filesystemScanner{root: root})
	records, err := scanner.scanRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Name != "docx" || records[0].Risk != toolshare.RiskSafe {
		t.Fatalf("records = %#v", records)
	}
}

func TestFilesystemScannerScansCallableAgentSkill(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\nrisk: low\n---\n\n# DOCX")
	writeAgentSkillConfig(t, dir, `risk = "medium"
command = ["python", "foo.py"]
parameters = '''{"type":"object","required":["input"],"properties":{"input":{"type":"string"}}}'''
[args]
input = "--input"
`)
	scanner := (filesystemScanner{root: root})
	records, err := scanner.scanRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Name != "docx" || records[0].Risk != toolshare.RiskMedium {
		t.Fatalf("records = %#v", records)
	}
	if !records[0].ManifestFound || !records[0].Manifest.Callable {
		t.Fatalf("callable manifest = %#v", records[0])
	}
}

func TestFilesystemScannerKeepsPolicyOnlyManifestAsDocumentSkill(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "private_doc")
	writeSkill(t, dir, "---\nname: private_doc\ndescription: Private doc\n---\n\n# Private")
	writeAgentSkillConfig(t, dir, `risk = "high"
superadmin_only = true
tags = ["private"]
`)
	scanner := (filesystemScanner{root: root})
	records, err := scanner.scanRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Name != "private_doc" || records[0].Risk != toolshare.RiskHigh || !records[0].SuperadminOnly {
		t.Fatalf("records = %#v", records)
	}
	if records[0].Manifest.Callable {
		t.Fatalf("document manifest = %#v", records[0])
	}
	if len(records[0].Tags) != 1 || records[0].Tags[0] != "private" {
		t.Fatalf("tags = %#v", records[0].Tags)
	}
}

func TestFilesystemScannerLeavesRiskForRegistration(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\nrisk: low\n---\n\n# DOCX")
	writeAgentSkillConfig(t, dir, `risk = "nope"
command = ["python", "foo.py"]
parameters = '''{"type":"object","properties":{"input":{"type":"string"}}}'''
[args]
input = "--input"
`)
	scanner := (filesystemScanner{root: root})
	records, err := scanner.scanRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Name != "docx" || records[0].Risk != "nope" {
		t.Fatalf("records = %#v", records)
	}
	if !records[0].ManifestFound || !records[0].Manifest.Callable {
		t.Fatalf("document manifest = %#v", records[0])
	}
}

func TestFilesystemScannerRejectsBrokenElyph(t *testing.T) {
	root := t.TempDir()
	brokenDir := filepath.Join(root, "broken")
	goodDir := filepath.Join(root, "good")
	writeElyphSkill(t, brokenDir, "#skill broken - Broken\ninvalid body\n")
	writeElyphSkill(t, goodDir, "#skill good - Good skill\n> done\n")
	scanner := (filesystemScanner{root: root})
	_, err := scanner.scanRecords(context.Background())
	if err == nil || !strings.Contains(err.Error(), brokenDir) {
		t.Fatalf("Scan error = %v", err)
	}
}

func TestFilesystemScannerCanceledEmptyDirectory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (filesystemScanner{root: t.TempDir()}).scanRecords(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("scanRecords = %v, want canceled", err)
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
