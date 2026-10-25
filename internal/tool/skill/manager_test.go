package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/processenv"
	"elbot/internal/security"
	"elbot/internal/tool/discovery"
	toolregistry "elbot/internal/tool/registry"
	toolshare "elbot/internal/tool/share"
)

func TestManagerSerializesConcurrentReloads(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "agent", "docs"), "---\nname: docs\ndescription: Docs\n---\n\n# Docs")
	writeElyphSkill(t, filepath.Join(root, "go", "worker"), "#skill worker - Worker\n")
	manager := NewManager(root, toolregistry.NewRegistry(nil), nil, processenv.Environment{})

	const goroutines = 12
	const reloads = 20
	errs := make(chan error, goroutines*reloads)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range reloads {
				errs <- manager.Reload(context.Background())
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Reload: %v", err)
		}
	}
	if got := manager.registry.List(); len(got) != 2 {
		t.Fatalf("registry = %#v, want two skills", got)
	}
	if got := manager.catalog.List(); len(got) != 2 {
		t.Fatalf("catalog = %#v, want two skills", got)
	}
}

func TestManagerRegistersSkillForms(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest string
		callable bool
		risk     toolshare.RiskLevel
	}{
		{name: "document", risk: toolshare.RiskSafe},
		{name: "policy", manifest: "risk = \"high\"\n", risk: toolshare.RiskHigh},
		{name: "invalid", manifest: "risk = \"invalid\"\n", risk: toolshare.RiskSafe},
		{
			name: "callable", callable: true, risk: toolshare.RiskMedium,
			manifest: `risk = "medium"
command = ["echo"]
parameters = '''{"type":"object","properties":{"input":{"type":"string"}}}'''
[args]
input = "--input"
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "agent", "docs")
			writeSkill(t, dir, "---\nname: docs\ndescription: Docs\n---\n\n# Docs")
			if tc.manifest != "" {
				writeAgentSkillConfig(t, dir, tc.manifest)
			}
			registry := toolregistry.NewRegistry(nil)
			manager := NewManager(root, registry, nil, processenv.Environment{})
			if err := manager.Reload(context.Background()); err != nil {
				t.Fatal(err)
			}
			target, ok := registry.Get("docs")
			if !ok || target.Info().Risk != tc.risk {
				t.Fatalf("registered skill = %#v, want risk %s", target, tc.risk)
			}
			if tc.callable {
				if _, ok := target.(CommandTool); !ok {
					t.Fatalf("registered type = %T, want CommandTool", target)
				}
			} else if _, ok := target.(Descriptor); !ok {
				t.Fatalf("registered type = %T, want Descriptor", target)
			}
		})
	}
}

func TestManagerLoadsMarkdownDetailOnDemand(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\n---\n\n# Initial DOCX")
	registry := toolregistry.NewRegistry(nil)
	manager := NewManager(root, registry, nil, processenv.Environment{})
	if err := manager.Reload(context.Background()); err != nil {
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
	result, _, err := discovery.NewDiscoverTool(discovery.NewResolver(registry)).Call(context.Background(), toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "# Updated DOCX") || strings.Contains(result.Content, "# Initial DOCX") {
		t.Fatalf("discovery should read current SKILL.md: %q", result.Content)
	}
}

func TestManagerLoadsCallableMarkdownDetailOnDemand(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\n---\n\n# Initial DOCX")
	writeAgentSkillConfig(t, dir, `risk = "safe"
command = ["echo"]
parameters = '''{"type":"object","properties":{"input":{"type":"string"}}}'''
[args]
input = "--input"
`)
	registry := toolregistry.NewRegistry(nil)
	manager := NewManager(root, registry, nil, processenv.Environment{})
	if err := manager.Reload(context.Background()); err != nil {
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
	result, _, err := discovery.NewDiscoverTool(discovery.NewResolver(registry)).Call(context.Background(), toolshare.CallRequest{Arguments: args})
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
	registry := toolregistry.NewRegistry(nil)
	manager := NewManager(root, registry, nil, processenv.Environment{})
	if err := manager.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"name": "docx"})
	_, _, err := discovery.NewDiscoverTool(discovery.NewResolver(registry)).Call(context.Background(), toolshare.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), "read SKILL.md") {
		t.Fatalf("discovery error = %v, want SKILL.md read failure", err)
	}
}

func TestPolicyOnlyAgentSkillHiddenFromNormalUserDiscovery(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "private_doc")
	writeSkill(t, dir, "---\nname: private_doc\ndescription: Private doc\n---\n\n# Private")
	writeAgentSkillConfig(t, dir, `risk = "high"
`)
	permission := security.NewPermission("low", "high", map[string][]string{"cli": {"local"}})
	registry := toolregistry.NewRegistry(permission)
	manager := NewManager(root, registry, nil, processenv.Environment{})
	err := manager.Reload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	actor := contextinfo.Actor{ID: "cli:guest", Platform: "cli", PlatformUserID: "guest", Role: contextinfo.RoleUser}
	ctx := contextinfo.WithActor(context.Background(), actor)
	discover := discovery.NewDiscoverTool(discovery.NewResolver(registry))
	result, _, err := discover.Call(ctx, toolshare.CallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result.Data), "private_doc") {
		t.Fatalf("private skill should be hidden from normal user: %s", result.Data)
	}
	args, _ := json.Marshal(map[string]string{"name": "private_doc"})
	if _, _, err := discover.Call(ctx, toolshare.CallRequest{Arguments: args}); err == nil {
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
	permission := security.NewPermission("low", "high", map[string][]string{"cli": {"local"}})
	registry := toolregistry.NewRegistry(permission)
	manager := NewManager(root, registry, nil, processenv.Environment{})
	err := manager.Reload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	superadmin := contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin}
	ctx := contextinfo.WithActor(context.Background(), superadmin)
	args, _ := json.Marshal(map[string]string{"name": "private_doc"})
	result, _, err := discovery.NewDiscoverTool(discovery.NewResolver(registry)).Call(ctx, toolshare.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "# Private") {
		t.Fatalf("superadmin should see skill detail: %q", result.Content)
	}
}

func TestManagerScansGoTextSkillWithoutBinary(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "go", "notes")
	writeElyphSkill(t, dir, "#skill notes - Notes workflow\n** risk low\n")
	registry := toolregistry.NewRegistry(nil)
	manager := NewManager(root, registry, nil, processenv.Environment{})
	err := manager.Reload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	target, ok := registry.Get("notes")
	if !ok || target.Info().Source != toolshare.SourceSkillGo || target.Info().Risk != toolshare.RiskLow {
		t.Fatalf("text skill = %#v", target)
	}
	detailer := target.(toolshare.DetailProvider)
	if len(detailer.ActivateTools()) != 0 {
		t.Fatalf("pure ELyph go skill should not activate runner: %#v", detailer.ActivateTools())
	}
}

func TestManagerReloadRemovesDeletedExternalSkill(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\n---\n\n# DOCX")
	registry := toolregistry.NewRegistry(nil)
	manager := NewManager(root, registry, nil, processenv.Environment{})
	if err := manager.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("docx"); !ok {
		t.Fatal("docx should be registered")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("docx"); ok {
		t.Fatal("docx should be unregistered")
	}
}

func TestManagerReloadConflictKeepsRegistryAndCatalog(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "agent", "old")
	writeSkill(t, oldDir, "---\nname: old\ndescription: Old skill\n---\n\n# Old")
	registry := toolregistry.NewRegistry(nil)
	manager := NewManager(root, registry, nil, processenv.Environment{})
	if err := registry.Register(NewAgentSkillTool(nil)); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(oldDir); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(root, "agent", AgentSkillManagerName), "---\nname: agent_skill\ndescription: Conflict\n---\n\n# Conflict")

	err := manager.Reload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "conflicts with existing source") {
		t.Fatalf("Reload error = %v, want builtin conflict", err)
	}
	if _, ok := registry.Get("old"); !ok {
		t.Fatal("old registry snapshot should remain after failed reload")
	}
	if _, ok := manager.catalog.Get("old"); !ok {
		t.Fatal("old catalog snapshot should remain after failed reload")
	}
	if _, ok := manager.catalog.Get(AgentSkillManagerName); ok {
		t.Fatal("conflicting skill should not enter catalog")
	}
}

func TestManagerReloadRejectsDuplicateSkillNames(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "agent", "shared"), "---\nname: shared\ndescription: Agent copy\n---\n\n# Shared")
	writeElyphSkill(t, filepath.Join(root, "go", "shared"), "#skill shared - Go copy\n")
	registry := toolregistry.NewRegistry(nil)
	manager := NewManager(root, registry, nil, processenv.Environment{})
	err := manager.Reload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "duplicate skill names") {
		t.Fatalf("Reload error = %v, want duplicate name error", err)
	}
	if len(registry.List()) != 0 || len(manager.catalog.List()) != 0 {
		t.Fatalf("failed reload changed state: registry=%#v catalog=%#v", registry.List(), manager.catalog.List())
	}
}

func TestManagerRemoveDeletesDirectoryAndReloads(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\nrisk: low\n---\n\n# DOCX")
	registry := toolregistry.NewRegistry(nil)
	manager := NewManager(root, registry, nil, processenv.Environment{})
	if err := manager.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(context.Background(), "docx"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("skill dir should be removed, err=%v", err)
	}
	if _, ok := registry.Get("docx"); ok {
		t.Fatal("docx should be unregistered after remove")
	}
}

func TestAgentDescriptorDetailWithoutActor(t *testing.T) {
	d := NewDescriptor(record{Name: "docx", Detail: "# DOCX", Kind: KindAgent})
	if d.Detail() != "# DOCX" || d.DetailBlock().Content != "# DOCX" || len(d.ActivateTools()) != 1 || d.ActivateTools()[0] != AgentSkillManagerName {
		t.Fatalf("detail=%q activate=%#v", d.Detail(), d.ActivateTools())
	}
}

func TestAgentSkillCreatorNoticeDiscovery(t *testing.T) {
	for _, invalidManifest := range []bool{false, true} {
		registry := toolregistry.NewRegistry(nil)
		record := record{Name: "docx", Detail: "# DOCX", Kind: KindAgent, Risk: toolshare.RiskSafe}
		if invalidManifest {
			record.ManifestFound = true
			record.ManifestError = "invalid manifest"
		}
		if err := registry.Register(NewDescriptor(record)); err != nil {
			t.Fatal(err)
		}
		discover := discovery.NewDiscoverTool(discovery.NewResolver(registry))
		for _, role := range []contextinfo.Role{contextinfo.RoleUser, contextinfo.RoleSuperadmin, ""} {
			t.Run(fmt.Sprintf("invalid=%t/role=%s", invalidManifest, role), func(t *testing.T) {
				ctx := context.Background()
				if role != "" {
					ctx = contextinfo.WithActor(ctx, contextinfo.Actor{Role: role})
				}
				result, _, err := discover.Call(ctx, toolshare.CallRequest{Arguments: json.RawMessage(`{"name":"docx"}`)})
				if err != nil {
					t.Fatal(err)
				}
				var discovery toolshare.DiscoveryResult
				if err := json.Unmarshal(result.Data, &discovery); err != nil {
					t.Fatal(err)
				}
				if len(discovery.Tools) != 1 {
					t.Fatalf("discovered tools = %#v", discovery.Tools)
				}
				for _, content := range []string{result.Content, discovery.Tools[0].Detail} {
					if !strings.Contains(content, "# DOCX") {
						t.Fatalf("missing skill body: %q", content)
					}
					if got := strings.Contains(content, "agent_skill_creator"); got != (role == contextinfo.RoleSuperadmin) {
						t.Fatalf("creator notice for %q: %q", role, content)
					}
					if got := strings.Contains(content, "invalid manifest"); got != invalidManifest {
						t.Fatalf("manifest diagnostic: %q", content)
					}
					if got := strings.Contains(content, "ElBot AgentSkill 使用提示："); got != (invalidManifest || role == contextinfo.RoleSuperadmin) {
						t.Fatalf("notice heading: %q", content)
					}
				}
			})
		}
	}
}
