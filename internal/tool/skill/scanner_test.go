package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/security"
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

func TestFilesystemScannerLoadsMarkdownDetailOnDemand(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\n---\n\n# Initial DOCX")
	scanner := NewFilesystemScanner(root)
	registry := tool.NewRegistry()
	if err := scanner.Reload(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	candidate, ok := registry.Get("docx")
	if !ok {
		t.Fatal("docx should be registered")
	}
	descriptor, ok := candidate.(Descriptor)
	if !ok {
		t.Fatalf("tool type = %T, want Descriptor", candidate)
	}
	if descriptor.Record.Detail != "" || descriptor.Record.DetailPath != filepath.Join(dir, "SKILL.md") {
		t.Fatalf("record detail should not be cached: %#v", descriptor.Record)
	}

	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\n---\n\n# Updated DOCX")
	args, _ := json.Marshal(map[string]string{"name": "docx"})
	result, err := tool.NewDiscoverTool(registry).Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "# Updated DOCX") || strings.Contains(result.Content, "# Initial DOCX") {
		t.Fatalf("discovery should read current SKILL.md: %q", result.Content)
	}
}

func TestFilesystemScannerLoadsCallableMarkdownDetailOnDemand(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\n---\n\n# Initial DOCX")
	writeAgentSkillConfig(t, dir, `risk = "safe"
command = ["echo"]
parameters = '''{"type":"object","properties":{"input":{"type":"string"}}}'''
[args]
input = "--input"
`)
	scanner := NewFilesystemScanner(root)
	registry := tool.NewRegistry()
	if err := scanner.Reload(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	candidate, ok := registry.Get("docx")
	if !ok {
		t.Fatal("docx should be registered")
	}
	command, ok := candidate.(CommandTool)
	if !ok {
		t.Fatalf("tool type = %T, want CommandTool", candidate)
	}
	if command.Record.Detail != "" || command.Record.DetailPath == "" {
		t.Fatalf("callable skill detail should not be cached: %#v", command.Record)
	}

	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\n---\n\n# Updated callable DOCX")
	args, _ := json.Marshal(map[string]string{"name": "docx"})
	result, err := tool.NewDiscoverTool(registry).Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "# Updated callable DOCX") {
		t.Fatalf("callable discovery should read current SKILL.md: %q", result.Content)
	}
}

func TestMarkdownDetailReadFailureReturnsDiscoveryError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\n---\n\n# DOCX")
	scanner := NewFilesystemScanner(root)
	registry := tool.NewRegistry()
	if err := scanner.Reload(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"name": "docx"})
	_, err := tool.NewDiscoverTool(registry).Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "read SKILL.md") {
		t.Fatalf("discovery error = %v, want SKILL.md read failure", err)
	}
}

func TestFilesystemScannerKeepsPolicyOnlyManifestAsDocumentSkill(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "private_doc")
	writeSkill(t, dir, "---\nname: private_doc\ndescription: Private doc\n---\n\n# Private")
	writeAgentSkillConfig(t, dir, `risk = "high"
superadmin_only = true
tags = ["private"]
`)
	scanner := NewFilesystemScanner(root)
	tools, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name() != "private_doc" || tools[0].Info().Risk != tool.RiskHigh || !tools[0].Info().SuperadminOnly {
		t.Fatalf("tools = %#v", tools)
	}
	if _, ok := tools[0].(Descriptor); !ok {
		t.Fatalf("tool type = %T, want Descriptor", tools[0])
	}
	if len(tools[0].Info().Tags) != 1 || tools[0].Info().Tags[0] != "private" {
		t.Fatalf("tags = %#v", tools[0].Info().Tags)
	}
}

func TestPolicyOnlyAgentSkillHiddenFromNormalUserDiscovery(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "private_doc")
	writeSkill(t, dir, "---\nname: private_doc\ndescription: Private doc\n---\n\n# Private")
	writeAgentSkillConfig(t, dir, `risk = "high"
`)
	scanner := NewFilesystemScanner(root)
	tools, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	for _, scanned := range tools {
		if err := registry.Register(scanned); err != nil {
			t.Fatal(err)
		}
	}
	policy := security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}})
	actor := security.Actor{ID: "cli:guest", Platform: "cli", PlatformUserID: "guest", Role: security.RoleUser}
	ctx := security.WithPolicy(security.WithActor(context.Background(), actor), policy)
	discover := tool.NewDiscoverTool(registry)
	result, err := discover.Call(ctx, tool.CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result.Data), "private_doc") {
		t.Fatalf("private skill should be hidden from normal user: %s", result.Data)
	}
	args, _ := json.Marshal(map[string]string{"name": "private_doc"})
	if _, err := discover.Call(ctx, tool.CallRequest{Arguments: args}); err == nil {
		t.Fatal("expected normal user detail query to be denied")
	}
}

func TestSuperadminOnlyAgentSkillVisibleToSuperadminDiscovery(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "private_doc")
	writeSkill(t, dir, "---\nname: private_doc\ndescription: Private doc\n---\n\n# Private")
	writeAgentSkillConfig(t, dir, `risk = "low"
superadmin_only = true
`)
	scanner := NewFilesystemScanner(root)
	tools, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	for _, scanned := range tools {
		if err := registry.Register(scanned); err != nil {
			t.Fatal(err)
		}
	}
	policy := security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}})
	superadmin := security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}
	ctx := security.WithPolicy(security.WithActor(context.Background(), superadmin), policy)
	args, _ := json.Marshal(map[string]string{"name": "private_doc"})
	result, err := tool.NewDiscoverTool(registry).Call(ctx, tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "# Private") {
		t.Fatalf("superadmin should see skill detail: %q", result.Content)
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
