package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/processenv"
	"elbot/internal/security"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

func TestCreateUsesSameGoForVersionAndBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses shell executables")
	}
	for _, selection := range []string{"configured", "goroot", "path"} {
		t.Run(selection, func(t *testing.T) {
			dir := t.TempDir()
			writeGo := func(name, version string) string {
				t.Helper()
				path := filepath.Join(dir, name, "bin", "go")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\nenv) printf 'go%s.0\\n' ;;\nbuild) printf '%s stdout\\n'; printf '%s stderr\\n' >&2; exit 7 ;;\n*) exit 9 ;;\nesac\n", version, name, name)
				if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				return path
			}
			pathGo := writeGo("path", "1.22")
			rootGo := writeGo("goroot", "1.23")
			configuredGo := writeGo("configured", "1.24")
			env := []string{"PATH=" + filepath.Dir(pathGo)}
			wantVersion := "1.22"
			if selection != "path" {
				env = append(env, "GOROOT="+filepath.Dir(filepath.Dir(rootGo)))
				wantVersion = "1.23"
			}
			if selection == "configured" {
				env = append(env, goBinaryEnv+"="+configuredGo)
				wantVersion = "1.24"
			}
			root := filepath.Join(dir, "skills")
			catalog := registry.NewRegistry(security.DefaultPermission())
			creator := CreateElSkillTool{manager: NewManager(root, catalog, nil, processenv.New(env))}
			args := []byte(`{"name":"compiler_probe","description":"Compiler selection.","risk":"low","elyph":"#skill compiler_probe - Compiler selection.\n<- $text:str!\n-> $result:str\n","go_source":"package main\nfunc main() {}"}`)
			result, summary, err := callTool(t.Context(), creator.Definition(), share.CallRequest{Arguments: args})
			if err == nil || result != nil || summary != "skill=compiler_probe build=failed exit_code=7" {
				t.Fatalf("build failure changed: result=%+v summary=%q err=%v", result, summary, err)
			}
			if text := err.Error(); !strings.Contains(text, "go build failed:") || !strings.Contains(text, selection+" stdout") || !strings.Contains(text, selection+" stderr") {
				t.Fatalf("wrong compiler or missing diagnostics: %v", err)
			}
			goMod, err := os.ReadFile(filepath.Join(root, "go", "compiler_probe", "go.mod"))
			if err != nil || !strings.Contains(string(goMod), "\ngo "+wantVersion+"\n") {
				t.Fatalf("wrong version: go.mod=%q err=%v", goMod, err)
			}
			if _, ok := catalog.Get("compiler_probe"); ok {
				t.Fatal("failed build was registered")
			}
		})
	}
}
