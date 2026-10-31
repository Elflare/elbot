package skill

import toolshare "elbot/internal/tool/share"

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSkillTruncationSeparatesModelAndLog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	for _, fail := range []bool{false, true} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "output.txt"), []byte(strings.Repeat("雪🔥", 5000)), 0600); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\ncat output.txt\n"
		if fail {
			script = "#!/bin/sh\ncat output.txt >&2\nexit 7\n"
		}
		binary := filepath.Join(root, "output.sh")
		if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		catalog := newCatalog()
		catalog.Replace([]record{{Name: "output", Kind: KindGo, Root: root, BinaryPath: binary}})
		callers := []toolshare.Tool{
			NewGoRunner(&Manager{media: newTestMediaRuntime(t), catalog: catalog}),
			commandTool{media: newTestMediaRuntime(t), record: record{Name: "output", Root: root}, manifest: AgentSkillManifest{Command: []string{"sh", "output.sh"}}},
		}
		for _, caller := range callers {
			result, logContent, err := callTool(t.Context(), caller, toolshare.CallRequest{Arguments: json.RawMessage(`{"skill_name":"output","payload":{}}`)})
			if fail {
				if err == nil || !strings.Contains(err.Error(), "output truncated") || !strings.Contains(err.Error(), "exit code 7") {
					t.Fatalf("%s lost model error details: %v", caller.Name(), err)
				}
			} else if err != nil || result == nil || !strings.HasSuffix(result.Content, "\n... output truncated ...\n") {
				t.Fatalf("%s lost model truncation notice: %v", caller.Name(), err)
			}
			preview := result.LogContent(err)
			if logContent != "" || strings.Contains(preview, "output truncated") || !strings.HasSuffix(preview, "…") || utf8.RuneCountInString(preview) != 1024 || !utf8.ValidString(preview) {
				t.Fatalf("%s summary=%q preview=%q", caller.Name(), logContent, preview)
			}
		}
	}
}
