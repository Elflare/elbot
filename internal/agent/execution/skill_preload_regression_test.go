package execution

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/security"
	"elbot/internal/tool"
	"elbot/internal/tool/skill"
)

func TestExecutableSkillPreloadAndCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	for _, backgroundRun := range []bool{false, true} {
		name := "foreground"
		if backgroundRun {
			name = "background"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("echo skill-command-ok\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			command := skill.NewCommandTool(skill.Record{
				Name: "echo_skill", Kind: skill.KindAgent, Root: root, Detail: "Use echo_skill to run the task.",
				Manifest: skill.AgentSkillManifest{Risk: tool.RiskLow, Command: []string{"sh", "run.sh"}, Parameters: map[string]any{"type": "object"}},
			})
			registry := tool.NewRegistry()
			for _, candidate := range []tool.Tool{command, skill.NewAgentSkillTool(nil)} {
				if err := registry.Register(candidate); err != nil {
					t.Fatal(err)
				}
			}
			f := &fakeLLM{chunks: [][]chatcompletions.Chunk{
				{{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "call_skill", Name: "echo_skill", Args: `{}`}}, FinishReason: "tool_calls"}},
				{{DeltaContent: "done"}},
			}}
			a := newTestAgent(t, &fakePlatform{}, f, "test-model", config.ProviderConfig{}, newTestStore(t), func(cfg *testAgentOptions) {
				cfg.SecurityPolicy = security.NewPolicy("low", "critical", map[string][]string{"cli": {"local"}})
				cfg.ToolsConfig = config.ToolsConfig{MaxRoundsPerTurn: 2}
				cfg.ToolRegistry = registry
			})
			if backgroundRun {
				_, err := a.RunBackground(t.Context(), background.RunRequest{
					Kind: background.KindCron, Name: "command-skill", Platform: "cli",
					Actor:  contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin},
					Prompt: "run", ToolListNames: []string{"echo_skill", "echo_skill"},
				})
				if err != nil {
					t.Fatal(err)
				}
			} else if err := a.HandleMessage(t.Context(), "run @skill:echo_skill"); err != nil {
				t.Fatal(err)
			}
			requests := f.chatRequests()
			if len(requests) != 2 {
				t.Fatalf("requests = %d, want 2", len(requests))
			}
			if got := toolNames(requests[0].Tools); got != "agent_skill,echo_skill" {
				t.Fatalf("first request tools = %q", got)
			}
			content := llm.SegmentsContentText(requests[0].Messages[len(requests[0].Messages)-1].Segments)
			if strings.Count(content, "Use echo_skill to run the task.") != 1 {
				t.Fatalf("skill instructions = %q", content)
			}
			for _, message := range requests[1].Messages {
				if message.Role == llm.RoleTool && message.Name == "echo_skill" && strings.Contains(llm.SegmentsContentText(message.Segments), "skill-command-ok") {
					return
				}
			}
			t.Fatal("skill command result did not reach the model")
		})
	}
}
