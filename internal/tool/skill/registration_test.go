package skill

import (
	"os"
	"path/filepath"
	"testing"

	"elbot/internal/processenv"
	"elbot/internal/security"
	"elbot/internal/tool/registry"
)

func TestInvalidCommandRegistrationPreservesPreviousCatalog(t *testing.T) {
	for _, risk := range []string{"", "typo"} {
		t.Run(risk, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "agent", "guide")
			writeSkill(t, dir, "---\nname: guide\ndescription: Guide\n---\n# Document only")
			catalog := registry.NewRegistry(security.DefaultPermission())
			manager := NewManager(root, catalog, nil, processenv.Environment{})
			if err := manager.Reload(t.Context()); err != nil {
				t.Fatal(err)
			}
			writeAgentSkillConfig(t, dir, "risk = \""+risk+"\"\ncommand = [\"echo\"]\nparameters = '''{\"type\":\"object\",\"properties\":{}}'''\n")
			if err := manager.Reload(t.Context()); err == nil {
				t.Fatal("invalid callable risk accepted")
			}
			tool, ok := catalog.Get("guide")
			if !ok || tool.Callable() || tool.LoadDetail == nil {
				t.Fatal("previous document replaced")
			}
			record, ok := manager.catalog.Get("guide")
			if !ok || record.ManifestFound {
				t.Fatal("failed reload published records")
			}
			if err := os.Remove(AgentSkillConfigPath(dir)); err != nil {
				t.Fatal(err)
			}
			if err := manager.Reload(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
