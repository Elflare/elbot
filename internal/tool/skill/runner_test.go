package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/config"
	"elbot/internal/processenv"
	"elbot/internal/tool"
)

func TestRunMediaCommandOutputLimits(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"plain exact", "plain overflow", "json exact", "json overflow", "stderr overflow", "failure", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ready := filepath.Join(t.TempDir(), "ready")
			type outcome struct {
				result *tool.Result
				err    error
			}
			done := make(chan outcome, 1)
			exited := make(chan struct{})
			go func() {
				defer close(exited)
				cmd := exec.CommandContext(ctx, binary, mode, ready)
				cmd.Env = append(os.Environ(), "ELBOT_SKILL_TEST_HELPER=output")
				result, err := runMediaCommand(ctx, "test skill", cmd, nil)
				done <- outcome{result, err}
			}()
			if mode == "canceled" {
				waitForSkillHelperReady(t, ready, exited)
				cancel()
			}
			out := <-done
			switch mode {
			case "plain exact", "json exact":
				if out.err != nil || out.result.Content != strings.Repeat("x", maxRunnerOutput)+"\n... output truncated ...\n" {
					t.Fatalf("exact limit result: %v", out.err)
				}
			case "stderr overflow":
				if out.err != nil || out.result.Content != "done" {
					t.Fatalf("stderr changed success: %#v, %v", out.result, out.err)
				}
			case "plain overflow", "json overflow":
				if out.result != nil || out.err == nil || !strings.Contains(out.err.Error(), "stdout exceeds 1048576 byte limit") {
					t.Fatalf("overflow result: %#v, %v", out.result, out.err)
				}
			default:
				if out.err == nil {
					t.Fatal("expected process error")
				}
				if mode == "canceled" && !errors.Is(out.err, context.Canceled) {
					t.Fatalf("cancellation classification: %v", out.err)
				}
				for _, want := range []string{"stdout-prefix", "stderr-prefix", "output truncated"} {
					if !strings.Contains(out.err.Error(), want) {
						t.Fatalf("diagnostics missing %q", want)
					}
				}
				if mode == "failure" && !strings.Contains(out.err.Error(), "exit code 7") {
					t.Fatalf("exit error: %v", out.err)
				}
				if len(out.err.Error()) > 2*maxRunnerOutput+512 {
					t.Fatal("diagnostic output exceeded display budget")
				}
			}
		})
	}
}

func TestSafeRelativePathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := safeRelativePath(root, "../evil.py"); err == nil {
		t.Fatal("expected path escape error")
	}
	if _, err := safeRelativePath(root, filepath.Join("scripts", "ok.py")); err != nil {
		t.Fatal(err)
	}
}

func TestCommandErrorPreservesUnderlyingCancellationFailure(t *testing.T) {
	for _, reason := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(reason.Error(), func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			if reason == context.Canceled {
				ctx, cancel = context.WithCancel(t.Context())
			} else {
				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			}
			cancel()
			failure := errors.New("terminate process group failed")
			stdout := truncateOutput(strings.Repeat("o", maxRunnerOutput+1))
			stderr := limitedOutput{limit: maxRunnerOutput}
			_, _ = stderr.Write([]byte(strings.Repeat("e", maxRunnerOutput+1)))
			err := commandError(ctx, "test skill", failure, stdout, stderr.String())
			if !errors.Is(err, reason) || !errors.Is(err, failure) {
				t.Fatalf("lost cancellation or termination failure: %v", err)
			}
			if !strings.Contains(err.Error(), "stdout:\n"+stdout) || !strings.Contains(err.Error(), "stderr:\n"+stderr.String()) || strings.Count(err.Error(), "output truncated") != 2 {
				t.Fatalf("lost command output: %v", err)
			}
			want := "test skill canceled:"
			if reason == context.DeadlineExceeded {
				want = "test skill timed out:"
			}
			if !strings.HasPrefix(err.Error(), want) || len(err.Error()) > 2*maxRunnerOutput+512 {
				t.Fatalf("incorrect cancellation diagnostics: %v", err)
			}
		})
	}
}

func TestSkillDeadlineSelection(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"go", "toml"} {
		for _, tc := range []struct {
			name   string
			value  int
			parent time.Duration
			want   time.Duration
		}{
			{name: "default", want: 30 * time.Second},
			{name: "explicit", value: 5, want: 5 * time.Second},
			{name: "shorter parent", value: 5, parent: 2 * time.Second, want: 2 * time.Second},
			{name: "longer parent", value: 5, parent: time.Minute, want: 5 * time.Second},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx := t.Context()
					if tc.parent > 0 {
						var cancel context.CancelFunc
						ctx, cancel = context.WithTimeout(ctx, tc.parent)
						defer cancel()
					}
					want := time.Now().Add(tc.want)
					calls := 0
					execute := func(ctx context.Context, _ string, _ *exec.Cmd, _ *tool.MediaCall) (*tool.Result, error) {
						calls++
						deadline, ok := ctx.Deadline()
						if !ok || !deadline.Equal(want) || ctx.Err() != nil {
							t.Fatalf("deadline=%v want=%v err=%v", deadline, want, ctx.Err())
						}
						<-ctx.Done()
						return nil, ctx.Err()
					}
					dir := t.TempDir()
					var err error
					if kind == "go" {
						catalog := NewCatalog()
						catalog.Replace([]Record{{Name: "deadline", Kind: KindGo, Root: dir, BinaryPath: binary}})
						runner := NewGoRunner(catalog)
						raw, _ := json.Marshal(map[string]any{"skill_name": "deadline", "timeout_ms": tc.value * 1000, "payload": map[string]any{}})
						_, _, err = runner.call(ctx, tool.CallRequest{Arguments: raw}, execute)
					} else {
						command := NewCommandTool(Record{Root: dir, Manifest: AgentSkillManifest{Command: []string{binary}, TimeoutSeconds: tc.value}})
						_, _, err = command.call(ctx, tool.CallRequest{}, execute)
					}
					if calls != 1 || !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("calls=%d error=%v", calls, err)
					}
				})
			})
		}
	}
}

func TestGoRunnerPassesPayloadJSONToBinary(t *testing.T) {
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalog()
	catalog.Replace([]Record{{Name: "echo", Kind: KindGo, Root: dir, BinaryPath: binary}})
	args := []byte(`{"skill_name":"echo","payload":{"input":"hello"}}`)
	environment := processenv.Environment{}.Overlay(map[string]string{"ELBOT_SKILL_TEST_HELPER": "payload"})
	result, _, err := NewGoRunner(catalog, environment).Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "hello" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestGoRunnerUsesSharedProcessEnvironment(t *testing.T) {
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalog()
	catalog.Replace([]Record{{Name: "environment", Kind: KindGo, Root: dir, BinaryPath: binary}})
	runner := NewGoRunner(catalog, processenv.New([]string{"ELBOT_GO_SKILL_ENV_TEST=from-shared", "ELBOT_SKILL_TEST_HELPER=shared-env"}))
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
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
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
			environment := processenv.New([]string{"ELBOT_SKILL_TEST_HELPER=environment", "ELBOT_SKILL_ENV_TEST=process", "ELBOT_SKILL_PROCESS_ONLY=process", "ELBOT_SKILL_EMPTY_TEST=process", "PATH=" + processPath}).Fill(dotEnv)
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
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte("PATH="+filepath.Dir(binary)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		caller := NewCommandTool(Record{Name: "environment", Root: root, Manifest: AgentSkillManifest{Command: []string{filepath.Base(binary)}}})
		caller.ProcessEnv = processenv.Environment{}.Overlay(map[string]string{"ELBOT_SKILL_TEST_HELPER": "environment"})
		if _, _, err := caller.Call(context.Background(), tool.CallRequest{}); err != nil {
			t.Fatalf("resolve command through skill PATH: %v", err)
		}
	})
}
