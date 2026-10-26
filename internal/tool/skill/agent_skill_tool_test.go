package skill

import "elbot/internal/security"
import "elbot/internal/processenv"

import toolshare "elbot/internal/tool/share"

import (
	"context"
	toolmedia "elbot/internal/tool/media"
	toolregistry "elbot/internal/tool/registry"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparedAgentSkillReadUsesExecutionContent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\n---\n\n# DOCX")
	writeAgentSkillConfig(t, dir, "risk = \"low\"\n")
	manager := NewManager(root, toolregistry.NewRegistry(security.DefaultPermission()), toolmedia.NewMediaRuntime(nil, ""), processenv.Environment{})
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader := NewAgentSkillTool(manager)
	prepared, err := reader.PrepareCall(t.Context(), toolshare.CallRequest{Arguments: []byte(`{"action":"read","name":"docx"}`)})
	if err != nil {
		t.Fatal(err)
	}
	writeAgentSkillConfig(t, dir, "risk = \"high\"\n")
	result, _, err := prepared.Call(t.Context())
	if err != nil || !strings.Contains(result.Content, "risk = \"high\"") {
		t.Fatalf("read = %+v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := prepared.Call(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	manager.catalog.Replace(nil)
	if _, _, err := prepared.Call(t.Context()); err == nil {
		t.Fatal("removed target was accepted")
	}
}

func TestAgentSkillWriteRollsBackConfigWhenReloadFails(t *testing.T) {
	for _, tc := range []struct {
		name       string
		oldContent string
		oldRisk    toolshare.RiskLevel
	}{
		{name: "existing", oldContent: "risk = \"low\"\n", oldRisk: toolshare.RiskLow},
		{name: "new", oldRisk: toolshare.RiskSafe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "agent", "docx")
			writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\n---\n\n# DOCX")
			path := AgentSkillConfigPath(dir)
			if tc.oldContent != "" {
				writeAgentSkillConfig(t, dir, tc.oldContent)
			}
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			manager := NewManager(root, registry, toolmedia.NewMediaRuntime(nil, ""), processenv.Environment{})
			if err := manager.Reload(context.Background()); err != nil {
				t.Fatal(err)
			}

			brokenPath := filepath.Join(root, "agent", "broken", "SKILL.md")
			if err := os.MkdirAll(brokenPath, 0o755); err != nil {
				t.Fatal(err)
			}
			args, err := json.Marshal(agentSkillArgs{Action: "write", Name: "docx", Toml: "risk = \"high\"\n"})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = callTool(context.Background(), NewAgentSkillTool(manager), toolshare.CallRequest{Arguments: args})
			if err == nil || !strings.Contains(err.Error(), "restored previous") {
				t.Fatalf("Call error = %v, want rollback error", err)
			}

			data, readErr := os.ReadFile(path)
			if tc.oldContent == "" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("new config should be removed, data=%q err=%v", data, readErr)
				}
			} else if readErr != nil || string(data) != tc.oldContent {
				t.Fatalf("config after rollback = %q, %v; want %q", data, readErr, tc.oldContent)
			}
			registered, ok := registry.Get("docx")
			if !ok || registered.Info().Risk != tc.oldRisk {
				t.Fatalf("registry risk = %#v, %v; want %s", registered, ok, tc.oldRisk)
			}
			record, ok := manager.catalog.Get("docx")
			if !ok || record.Risk != tc.oldRisk {
				t.Fatalf("catalog record = %#v, %v; want risk %s", record, ok, tc.oldRisk)
			}
		})
	}
}

func TestAgentSkillWriteCommitsConfigAndReload(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "docx")
	writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\n---\n\n# DOCX")
	registry := toolregistry.NewRegistry(security.DefaultPermission())
	manager := NewManager(root, registry, toolmedia.NewMediaRuntime(nil, ""), processenv.Environment{})
	if err := manager.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	content := "risk = \"high\"\nsuperadmin_only = true\n"
	args, err := json.Marshal(agentSkillArgs{Action: "write", Name: "docx", Toml: content})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := callTool(context.Background(), NewAgentSkillTool(manager), toolshare.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(AgentSkillConfigPath(dir))
	if err != nil || string(data) != content {
		t.Fatalf("written config = %q, %v; want %q", data, err, content)
	}
	registered, ok := registry.Get("docx")
	if !ok || registered.Info().Risk != toolshare.RiskHigh || !registered.Info().SuperadminOnly {
		t.Fatalf("registry tool = %#v, %v", registered, ok)
	}
	record, ok := manager.catalog.Get("docx")
	if !ok || record.Risk != toolshare.RiskHigh || !record.SuperadminOnly {
		t.Fatalf("catalog record = %#v, %v", record, ok)
	}
}
