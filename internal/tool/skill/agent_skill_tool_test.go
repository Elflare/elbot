package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/tool"
)

func TestAgentSkillWriteRollsBackConfigWhenReloadFails(t *testing.T) {
	for _, tc := range []struct {
		name       string
		oldContent string
		oldRisk    tool.RiskLevel
	}{
		{name: "existing", oldContent: "risk = \"low\"\n", oldRisk: tool.RiskLow},
		{name: "new", oldRisk: tool.RiskSafe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "agent", "docx")
			writeSkill(t, dir, "---\nname: docx\ndescription: DOCX skill\n---\n\n# DOCX")
			path := AgentSkillConfigPath(dir)
			if tc.oldContent != "" {
				writeAgentSkillConfig(t, dir, tc.oldContent)
			}
			registry := tool.NewRegistry()
			manager := NewManager(root, registry)
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
			_, err = NewAgentSkillTool(manager).Call(context.Background(), tool.CallRequest{Arguments: args})
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
			record, ok := manager.Catalog.Get("docx")
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
	registry := tool.NewRegistry()
	manager := NewManager(root, registry)
	if err := manager.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	content := "risk = \"high\"\nsuperadmin_only = true\n"
	args, err := json.Marshal(agentSkillArgs{Action: "write", Name: "docx", Toml: content})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewAgentSkillTool(manager).Call(context.Background(), tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(AgentSkillConfigPath(dir))
	if err != nil || string(data) != content {
		t.Fatalf("written config = %q, %v; want %q", data, err, content)
	}
	registered, ok := registry.Get("docx")
	if !ok || registered.Info().Risk != tool.RiskHigh || !registered.Info().SuperadminOnly {
		t.Fatalf("registry tool = %#v, %v", registered, ok)
	}
	record, ok := manager.Catalog.Get("docx")
	if !ok || record.Risk != tool.RiskHigh || !record.SuperadminOnly {
		t.Fatalf("catalog record = %#v, %v", record, ok)
	}
}
