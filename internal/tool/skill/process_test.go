package skill

import "elbot/internal/security"

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/operationresult"
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
	toolregistry "elbot/internal/tool/registry"
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
				result operationresult.Result[*toolshare.Result]
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
				if out.err != nil || out.result.Status != operationresult.Success || out.result.Data.Content != strings.Repeat("x", maxRunnerOutput)+"\n... output truncated ...\n" {
					t.Fatalf("exact limit result: %v", out.err)
				}
			case "stderr overflow":
				if out.err != nil || out.result.Status != operationresult.Success || out.result.Data.Content != "done" {
					t.Fatalf("stderr changed success: %#v, %v", out.result, out.err)
				}
			case "plain overflow", "json overflow":
				if out.err != nil || out.result.Status != operationresult.Failed || !strings.Contains(out.result.Message, "stdout exceeds 1048576 byte limit") {
					t.Fatalf("overflow result: %#v, %v", out.result, out.err)
				}
			default:
				if out.err != nil {
					t.Fatal(out.err)
				}
				want := operationresult.Failed
				if mode == "canceled" {
					want = operationresult.Canceled
				}
				if out.result.Status != want {
					t.Fatalf("outcome=%+v", out.result)
				}
				for _, want := range []string{"stdout-prefix", "stderr-prefix", "output truncated"} {
					if !strings.Contains(out.result.Message, want) {
						t.Fatalf("diagnostics missing %q", want)
					}
				}
				if mode == "failure" && !strings.Contains(out.result.Message, "exit status 7") {
					t.Fatalf("exit outcome=%+v", out.result)
				}
				if len(out.result.Message) > 2*maxRunnerOutput+512 {
					t.Fatal("diagnostic output exceeded display budget")
				}
			}
		})
	}
}

func TestCommandErrorPreservesOriginalFailureAndOutput(t *testing.T) {
	failure := errors.New("process failed")
	stdout := truncateOutput(strings.Repeat("o", maxRunnerOutput+1))
	stderr := limitedOutput{limit: maxRunnerOutput}
	_, _ = stderr.Write([]byte(strings.Repeat("e", maxRunnerOutput+1)))
	err := commandError("test skill", failure, stdout, stderr.String())
	if !errors.Is(err, failure) {
		t.Fatalf("lost original failure: %v", err)
	}
	if !strings.Contains(err.Error(), "stdout:\n"+stdout) || !strings.Contains(err.Error(), "stderr:\n"+stderr.String()) || strings.Count(err.Error(), "output truncated") != 2 {
		t.Fatalf("lost output: %v", err)
	}
}

func TestSkillDeadlineSelection(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
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
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				if tc.parent > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tc.parent)
					defer cancel()
				}
				want := time.Now().Add(tc.want)
				calls := 0
				execute := func(ctx context.Context, _ string, _ *exec.Cmd, _ toolshare.MediaCall) (operationresult.Result[*toolshare.Result], error) {
					calls++
					deadline, ok := ctx.Deadline()
					if !ok || !deadline.Equal(want) || ctx.Err() != nil {
						t.Fatalf("deadline=%v want=%v err=%v", deadline, want, ctx.Err())
					}
					<-ctx.Done()
					return operationresult.Result[*toolshare.Result]{}, ctx.Err()
				}
				dir := t.TempDir()
				var err error
				timeout := tc.value
				if timeout == 0 {
					timeout = int(defaultRunnerTimeout.Seconds())
				}
				command := commandTool{record: record{Root: dir}, manifest: AgentSkillManifest{Command: []string{binary}, TimeoutSeconds: timeout}, media: newTestMediaRuntime(t)}
				_, _, err = command.call(ctx, nil, nil, dir, execute)
				if calls != 1 || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("calls=%d error=%v", calls, err)
				}
			})
		})
	}
}

func resultFromStdout(out string) (operationresult.Result[*toolshare.Result], error) {
	return resultFromStdoutWithMedia(context.Background(), out, nil, "")
}

func TestResultFromStdoutHandlesTextAndJSON(t *testing.T) {
	for _, tc := range []struct {
		stdout, content string
		status          operationresult.Status
	}{
		{"ok", "ok", operationresult.Success},
		{"", "", operationresult.Success},
		{"{\"content\":\"done\",\"data\":{\"path\":\"out.txt\"}}", "done", operationresult.Success},
		{"{\"content\":\"\"}", "", operationresult.Success},
		{"{\"segments\":[]}", "", operationresult.Success},
		{"{\"content\":\"\",\"segments\":[]}", "", operationresult.Success},
		{"{\"other\":true}", "{\"other\":true}", operationresult.Success},
		{"{\"content\":42}", "", operationresult.Failed},
		{"{\"segments\":\"invalid\"}", "", operationresult.Failed},
	} {
		result, err := resultFromStdout(tc.stdout)
		if err != nil || result.Status != tc.status {
			t.Fatalf("stdout=%q result=%+v err=%v", tc.stdout, result, err)
		}
		if tc.status == operationresult.Success {
			if result.Data.Content != tc.content || result.Data.Discovery != nil {
				t.Fatalf("result=%+v", result.Data)
			}
		} else if result.Message == "" || result.Data != nil {
			t.Fatalf("failure=%+v", result)
		}
	}
}

func TestSkillExecutionEnvironment(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ELBOT_SKILL_ENV_TEST", "host")

	t.Run("dotenv", func(t *testing.T) {
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
		newCaller := func(root string) (toolshare.ToolDefinition, toolshare.CallRequest) {
			t.Helper()
			writeSkill(t, root, "---\nname: environment\ndescription: Environment test\n---\n")
			command, err := json.Marshal([]string{binary})
			if err != nil {
				t.Fatal(err)
			}
			writeAgentSkillConfig(t, root, fmt.Sprintf("risk = \"low\"\ncommand = %s\nparameters = '''{\"type\":\"object\",\"properties\":{\"input\":{\"type\":\"string\"}}}'''\n[args]\ninput = \"--input\"\n", command))
			registry := toolregistry.NewRegistry(security.DefaultPermission())
			manager := NewManager(filepath.Dir(root), registry, newTestMediaRuntime(t), environment)
			for range 2 {
				if err := manager.Reload(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			candidate, ok := registry.Get("environment")
			if !ok {
				t.Fatal("environment command was not registered")
			}
			return candidate, toolshare.CallRequest{}
		}
		root := filepath.Join(configDir, "skills", "environment")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		caller, req := newCaller(root)
		check := func(caller toolshare.ToolDefinition, req toolshare.CallRequest, root, common, local, empty, path string) {
			t.Helper()
			result, _, err := callTool(context.Background(), caller, req)
			if err != nil || result.Status != operationresult.Success {
				t.Fatal(err)
			}
			var got map[string]string
			if err := json.Unmarshal([]byte(result.Data.Content), &got); err != nil {
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
		otherRoot := filepath.Join(t.TempDir(), "skills", "environment")
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
		if _, _, err := callTool(context.Background(), caller, req); err == nil || !strings.Contains(err.Error(), envPath) {
			t.Fatalf("unreadable dotenv error = %v, want path %s", err, envPath)
		}
		if _, err := os.Stat(filepath.Join(root, "executed")); !os.IsNotExist(err) {
			t.Fatalf("skill ran despite unreadable dotenv: %v", err)
		}
		if got := os.Getenv("ELBOT_SKILL_ENV_TEST"); got != "host" {
			t.Fatalf("host environment changed to %q", got)
		}
	})

	t.Run("agent-skill-path", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte("PATH="+filepath.Dir(binary)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		caller := commandTool{record: record{Name: "environment", Root: root}, manifest: AgentSkillManifest{Command: []string{filepath.Base(binary)}, TimeoutSeconds: 30}, media: newTestMediaRuntime(t)}
		caller.processEnv = processenv.Environment{}.Overlay(map[string]string{"ELBOT_SKILL_TEST_HELPER": "environment"})
		if result, _, err := callTool(context.Background(), caller.Definition(), toolshare.CallRequest{}); err != nil || result.Status != operationresult.Success {
			t.Fatalf("resolve command through skill PATH: %v", err)
		}
	})
}
