package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/processenv"
	"elbot/internal/tool"
)

func TestSafeRelativePathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := safeRelativePath(root, "../evil.py"); err == nil {
		t.Fatal("expected path escape error")
	}
	if _, err := safeRelativePath(root, filepath.Join("scripts", "ok.py")); err != nil {
		t.Fatal(err)
	}
}

func TestGoRunnerPassesPayloadJSONToBinary(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte(`package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	var payload map[string]string
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		fmt.Println("decode failed")
		os.Exit(1)
	}
	fmt.Printf(`+"`"+`{"content":"%s"}`+"`"+`, payload["input"])
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "helper")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}
	catalog := NewCatalog()
	catalog.Replace([]Record{{Name: "echo", Kind: KindGo, Root: dir, BinaryPath: binary}})
	args := []byte(`{"skill_name":"echo","payload":{"input":"hello"}}`)
	result, _, err := NewGoRunner(catalog).Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "hello" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestGoRunnerUsesSharedProcessEnvironment(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte(`package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Printf("{\"content\":\"%s\"}", os.Getenv("ELBOT_GO_SKILL_ENV_TEST"))
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "helper")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\\n%s", err, output)
	}
	catalog := NewCatalog()
	catalog.Replace([]Record{{Name: "environment", Kind: KindGo, Root: dir, BinaryPath: binary}})
	runner := NewGoRunner(catalog, processenv.New([]string{"ELBOT_GO_SKILL_ENV_TEST=from-shared"}))
	result, _, err := runner.Call(context.Background(), tool.CallRequest{Arguments: []byte(`{"skill_name":"environment","payload":{}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "from-shared" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestGoRunnerRejectsMissingPayload(t *testing.T) {
	_, err := parseGoRunnerArgs([]byte(`{"skill_name":"echo","input":"hello"}`))
	if err == nil || !strings.Contains(err.Error(), "payload is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandErrorIncludesSkillOutput(t *testing.T) {
	err := commandError(context.Background(), "go skill", &exec.ExitError{}, "bad stdout", "bad stderr")
	text := err.Error()
	for _, want := range []string{"go skill process failed", "stdout:\nbad stdout", "stderr:\nbad stderr"} {
		if !strings.Contains(text, want) {
			t.Fatalf("error %q missing %q", text, want)
		}
	}
}

func resultFromStdout(out string) (*tool.Result, error) {
	return resultFromStdoutWithMedia(context.Background(), out, nil, "")
}

func TestResultFromStdoutHandlesTextAndJSON(t *testing.T) {
	plain, err := resultFromStdout("ok")
	if err != nil || plain.Content != "ok" || len(plain.Data) != 0 {
		t.Fatalf("plain=%#v err=%v", plain, err)
	}
	structured, err := resultFromStdout(`{"content":"done","data":{"path":"out.txt"}}`)
	if err != nil || structured.Content != "done" || len(structured.Data) != 0 {
		t.Fatalf("structured=%#v err=%v", structured, err)
	}
}

func TestSkillExecutionEnvironment(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte(`package main

import (
	"encoding/json"
	"os"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil { panic(err) }
	if err := os.WriteFile("executed", nil, 0600); err != nil { panic(err) }
	values := map[string]string{"cwd": cwd}
	for _, name := range []string{"ELBOT_SKILL_ENV_TEST", "ELBOT_SKILL_CONFIG_ONLY", "ELBOT_SKILL_PROCESS_ONLY", "ELBOT_SKILL_LOCAL_ONLY", "ELBOT_SKILL_EMPTY_TEST", "PATH"} {
		values[name] = os.Getenv(name)
	}
	content, err := json.Marshal(values)
	if err != nil { panic(err) }
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"content": string(content)}); err != nil { panic(err) }
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "environment-helper")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", binary, source).CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}
	t.Setenv("ELBOT_SKILL_ENV_TEST", "host")

	for _, kind := range []string{"agent", "go"} {
		t.Run(kind, func(t *testing.T) {
			configDir := t.TempDir()
			processPath := filepath.Join(configDir, "process-bin")
			configPath := filepath.Join(configDir, "config-bin")
			if err := os.WriteFile(filepath.Join(configDir, ".env"), []byte("ELBOT_SKILL_ENV_TEST=config\nELBOT_SKILL_CONFIG_ONLY=config\nELBOT_SKILL_EMPTY_TEST=config\nPATH="+configPath+string(os.PathListSeparator)+processPath+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			dotEnv, err := config.LoadDotEnv(configDir)
			if err != nil {
				t.Fatal(err)
			}
			environment := processenv.New([]string{"ELBOT_SKILL_ENV_TEST=process", "ELBOT_SKILL_PROCESS_ONLY=process", "ELBOT_SKILL_EMPTY_TEST=process", "PATH=" + processPath}).Fill(dotEnv)
			newCaller := func(root string) (tool.Tool, tool.CallRequest) {
				t.Helper()
				if kind == "go" {
					catalog := NewCatalog()
					catalog.Replace([]Record{{Name: "environment", Kind: KindGo, Root: root, BinaryPath: binary}})
					return NewGoRunner(catalog, environment), tool.CallRequest{Arguments: []byte(`{"skill_name":"environment","payload":{}}`)}
				}
				writeSkill(t, root, "---\nname: environment\ndescription: Environment test\n---\n")
				command, err := json.Marshal([]string{binary})
				if err != nil {
					t.Fatal(err)
				}
				writeAgentSkillConfig(t, root, fmt.Sprintf("risk = \"low\"\ncommand = %s\nparameters = '''{\"type\":\"object\",\"properties\":{\"input\":{\"type\":\"string\"}}}'''\n[args]\ninput = \"--input\"\n", command))
				manager := NewManager(filepath.Dir(filepath.Dir(root)), tool.NewRegistry(), environment)
				tools, err := manager.Scanner.Scan(context.Background())
				if err != nil || len(tools) != 1 {
					t.Fatalf("scan: tools=%v, err=%v", tools, err)
				}
				// Verify execution through both scanning and registry replacement.
				if _, _, err := tools[0].Call(context.Background(), tool.CallRequest{}); err != nil {
					t.Fatalf("scanned command: %v", err)
				}
				for range 2 {
					if err := manager.Reload(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				candidate, ok := manager.Registry.Get("environment")
				if !ok {
					t.Fatal("environment command was not registered")
				}
				return candidate, tool.CallRequest{}
			}
			root := filepath.Join(configDir, "skills", kind, "environment")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			caller, req := newCaller(root)
			check := func(caller tool.Tool, req tool.CallRequest, root, common, local, empty, path string) {
				t.Helper()
				result, _, err := caller.Call(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				var got map[string]string
				if err := json.Unmarshal([]byte(result.Content), &got); err != nil {
					t.Fatal(err)
				}
				want := map[string]string{"cwd": root, "ELBOT_SKILL_ENV_TEST": common, "ELBOT_SKILL_CONFIG_ONLY": "config", "ELBOT_SKILL_PROCESS_ONLY": "process", "ELBOT_SKILL_LOCAL_ONLY": local, "ELBOT_SKILL_EMPTY_TEST": empty, "PATH": path}
				for name, value := range want {
					if got[name] != value {
						t.Errorf("%s = %q, want %q", name, got[name], value)
					}
				}
			}
			basePath := processPath + string(os.PathListSeparator) + configPath
			check(caller, req, root, "process", "", "process", basePath)
			localPath := filepath.Join(root, "bin")
			envPath := filepath.Join(root, ".env")
			writeEnv := func(content string) {
				t.Helper()
				if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writeEnv("ELBOT_SKILL_ENV_TEST=skill\nELBOT_SKILL_LOCAL_ONLY=local\nELBOT_SKILL_EMPTY_TEST=\nPATH=" + localPath + string(os.PathListSeparator) + configPath + "\n")
			check(caller, req, root, "skill", "local", "", basePath+string(os.PathListSeparator)+localPath)
			otherRoot := filepath.Join(t.TempDir(), "skills", kind, "environment")
			if err := os.MkdirAll(otherRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			otherCaller, otherReq := newCaller(otherRoot)
			check(otherCaller, otherReq, otherRoot, "process", "", "process", basePath)
			writeEnv("ELBOT_SKILL_ENV_TEST=updated\n")
			check(caller, req, root, "updated", "", "process", basePath)
			if err := os.Remove(envPath); err != nil {
				t.Fatal(err)
			}
			check(caller, req, root, "process", "", "process", basePath)
			if err := os.Remove(filepath.Join(root, "executed")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(envPath, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, _, err := caller.Call(context.Background(), req); err == nil || !strings.Contains(err.Error(), envPath) {
				t.Fatalf("unreadable dotenv error = %v, want path %s", err, envPath)
			}
			if _, err := os.Stat(filepath.Join(root, "executed")); !os.IsNotExist(err) {
				t.Fatalf("skill ran despite unreadable dotenv: %v", err)
			}
			if got := os.Getenv("ELBOT_SKILL_ENV_TEST"); got != "host" {
				t.Fatalf("host environment changed to %q", got)
			}
		})
	}

	t.Run("agent-skill-path", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte("PATH="+dir+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		caller := NewCommandTool(Record{Name: "environment", Root: root, Manifest: AgentSkillManifest{Command: []string{filepath.Base(binary)}}})
		if _, _, err := caller.Call(context.Background(), tool.CallRequest{}); err != nil {
			t.Fatalf("resolve command through skill PATH: %v", err)
		}
	})
}
