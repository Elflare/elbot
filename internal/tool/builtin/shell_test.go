package builtin

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
	"time"

	"elbot/internal/processenv"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/tool"
	workspacepath "elbot/internal/workspace"
)

func TestShellToolMissingCmdHintsExpectedArgument(t *testing.T) {
	shell := NewShellTool()
	args, _ := json.Marshal(map[string]any{"Command": "ls"})
	_, err := shell.AssessRisk(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), `use {"cmd":"..."}`) {
		t.Fatalf("AssessRisk error = %v", err)
	}
	_, err = shell.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err == nil || !strings.Contains(err.Error(), `use {"cmd":"..."}`) {
		t.Fatalf("Call error = %v", err)
	}
}

func TestShellToolHasAgentTag(t *testing.T) {
	if got := strings.Join(NewShellTool().Info().Tags, ","); got != "agent" {
		t.Fatalf("shell tags = %q", got)
	}
}

func TestShellToolRunsArbitraryCommand(t *testing.T) {
	shell := NewShellTool()
	args, _ := json.Marshal(map[string]any{"cmd": "echo elbot-shell-test"})
	result, err := shell.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !strings.Contains(result.Content, "elbot-shell-test") {
		t.Fatalf("unexpected shell result: %#v", result)
	}
}

func TestShellOutputBufferDrainsWithBoundedMemory(t *testing.T) {
	const limit = 256 * 1024
	for _, chunkSize := range []int{0, 1, 4093, limit + 1} {
		t.Run(fmt.Sprint(chunkSize), func(t *testing.T) {
			var output shellOutputBuffer
			chunk := []byte(strings.Repeat("x", chunkSize))
			total := 0
			for range 300 {
				n, err := output.Write(chunk)
				if err != nil || n != len(chunk) {
					t.Fatalf("write = %d, %v; want %d, nil", n, err, len(chunk))
				}
				total += n
				if len(output.data) != min(total, limit) || cap(output.data) > limit {
					t.Fatalf("retained=%d capacity=%d total=%d", len(output.data), cap(output.data), total)
				}
			}
			want := strings.Repeat("x", min(total, limit))
			if total > limit {
				want += "\n... output too long; truncated to first 256 KiB ...\n"
			}
			if output.String() != want {
				t.Fatal("retained prefix or truncation notice changed")
			}
		})
	}
}

func TestShellToolOutputLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell commands")
	}
	const limit = 256 * 1024
	const notice = "\n... output too long; truncated to first 256 KiB ...\n"
	for _, size := range []int{20 * 1024, limit, limit + 1, 4 * limit} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			dir := t.TempDir()
			out, errText := strings.Repeat("o", size), strings.Repeat("e", size)
			for name, content := range map[string]string{"stdout.txt": out, "stderr.txt": errText} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx := workspacepath.WithWorkspaceStore(t.Context(), &testWorkspaceStore{dir: dir})
			args, _ := json.Marshal(map[string]any{"cmd": "cat stdout.txt & cat stderr.txt >&2 & wait; exit 7"})
			result, err := NewShellTool().Call(ctx, tool.CallRequest{Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			if size > limit {
				out, errText = out[:limit]+notice, errText[:limit]+notice
			}
			want := out + "\nstderr:\n" + errText + "\nexit_code: 7"
			if result.Content != want {
				t.Fatalf("output mismatch: got %d bytes, want %d; truncations=%d", len(result.Content), len(want), strings.Count(result.Content, notice))
			}
		})
	}
}

func TestAnalyzeBashShellAdviceWarnsForCat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	advice := analyzeBashShellAdvice("cat "+filepath.ToSlash(path), filepath.Dir(path), nil)
	text := tool.AppendWarnings("", advice.warnings)
	if !strings.Contains(text, "read_file") {
		t.Fatalf("expected read_file warning, got:\n%s", text)
	}
}

func TestAnalyzeBashShellAdviceWarnsForCatElSkill(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "go", "reader", "SKILL.elyph")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#skill reader - Reader.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	advice := analyzeBashShellAdvice("cat "+filepath.ToSlash(path), root, NewFileGuard(NewElSkillFileGuardRule(root)))
	text := tool.AppendWarnings("", advice.warnings)
	if !strings.Contains(text, "read_file") || !strings.Contains(text, "read_el_skill") {
		t.Fatalf("expected read_file and read_el_skill warnings, got:\n%s", text)
	}
}

func TestAnalyzeBashShellAdviceRejectsSedEditElSkill(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "go", "writer", "SKILL.elyph")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := "#skill writer - Writer.\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	advice := analyzeBashShellAdvice("sed -i 's/Writer/Changed/' "+filepath.ToSlash(path), root, NewFileGuard(NewElSkillFileGuardRule(root)))
	if advice.blockErr == nil || !strings.Contains(advice.blockErr.Error(), "modify_el_skill") {
		t.Fatalf("expected modify_el_skill error, got %v", advice.blockErr)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed: %q", string(content))
	}
}

func TestAnalyzeBashShellAdviceWarnsForCatResidentMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	if err := os.WriteFile(path, []byte("[[resident_memories]]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	advice := analyzeBashShellAdvice("cat "+filepath.ToSlash(path), filepath.Dir(path), NewFileGuard(NewResidentMemoryFileGuardRule(path)))
	text := tool.AppendWarnings("", advice.warnings)
	if !strings.Contains(text, "read_file") || !strings.Contains(text, "resident_memory_read") {
		t.Fatalf("expected read_file and resident_memory_read warnings, got:\n%s", text)
	}
}

func TestAnalyzeBashShellAdviceRejectsRedirectToResidentMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.toml")
	original := "[[resident_memories]]\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	advice := analyzeBashShellAdvice("echo changed > "+filepath.ToSlash(path), filepath.Dir(path), NewFileGuard(NewResidentMemoryFileGuardRule(path)))
	if advice.blockErr == nil || !strings.Contains(advice.blockErr.Error(), "resident_memory_normal") {
		t.Fatalf("expected resident memory error, got %v", advice.blockErr)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file changed: %q", string(content))
	}
}

type testWorkspaceStore struct {
	dir        string
	noticeDirs []string
}

func (s *testWorkspaceStore) GetWorkspaceDir(ctx context.Context) (string, error) {
	return s.dir, nil
}

func (s *testWorkspaceStore) SetWorkspaceDir(ctx context.Context, dir string) error {
	s.dir = dir
	return nil
}

func (s *testWorkspaceStore) ClearWorkspaceDir(ctx context.Context) error {
	s.dir = ""
	return nil
}

func (s *testWorkspaceStore) HasWorkspaceAgentNoticeDir(ctx context.Context, dir string) (bool, error) {
	for _, noticeDir := range s.noticeDirs {
		if noticeDir == dir {
			return true, nil
		}
	}
	return false, nil
}

func (s *testWorkspaceStore) MarkWorkspaceAgentNoticeDir(ctx context.Context, dir string) error {
	return s.markWorkspaceAgentNoticeDir(dir, true)
}

func (s *testWorkspaceStore) SetWorkspaceDirWithAgentNotice(ctx context.Context, dir string, markNotice bool) error {
	s.dir = dir
	return s.markWorkspaceAgentNoticeDir(dir, markNotice)
}

func (s *testWorkspaceStore) ClearWorkspaceDirWithAgentNotice(ctx context.Context, dir string, markNotice bool) error {
	s.dir = ""
	return s.markWorkspaceAgentNoticeDir(dir, markNotice)
}

func (s *testWorkspaceStore) markWorkspaceAgentNoticeDir(dir string, markNotice bool) error {
	if !markNotice {
		return nil
	}
	for _, noticeDir := range s.noticeDirs {
		if noticeDir == dir {
			return nil
		}
	}
	s.noticeDirs = append(s.noticeDirs, dir)
	return nil
}

func TestShellToolUsesWorkspaceDir(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	ctx := workspacepath.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: workspace})
	shell := NewShellTool()
	args, _ := json.Marshal(map[string]any{"cmd": "echo workspace > workspace.txt"})
	if _, err := shell.Call(ctx, tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "workspace.txt")); err != nil {
		t.Fatalf("expected command to run in workspace %q: %v", workspace, err)
	}
}

func TestRejectBashShellDirectoryChangeCommand(t *testing.T) {
	err := rejectBashShellDirectoryChange("cd internal && pwd")
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("expected cd rejection, got %v", err)
	}
}

func TestAnalyzeBashShellAdviceWarnsForCommandPathArgument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0644); err != nil {
		t.Fatal(err)
	}
	advice := analyzeBashShellAdvice("cat "+filepath.ToSlash(path), filepath.Dir(path), nil)
	text := tool.AppendWarnings("", advice.warnings)
	if !strings.Contains(text, warnUseWorkspace) {
		t.Fatalf("expected path argument warning, got:\n%s", text)
	}
}

func TestShellToolUsesSandboxDir(t *testing.T) {
	sandboxDir := filepath.Join(t.TempDir(), "sandbox", "cron")
	shell := NewShellTool()
	args, _ := json.Marshal(map[string]any{"cmd": "pwd > cwd.txt"})
	ctx := sandboxctx.WithSandboxContext(context.Background(), sandboxctx.SandboxContext{Dir: sandboxDir, Background: true, BackgroundKind: sandboxctx.BackgroundKindCron})
	if _, err := shell.Call(ctx, tool.CallRequest{Arguments: args}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(sandboxDir, "cwd.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) == "" {
		t.Fatal("expected sandbox command to write cwd.txt")
	}
}

func TestShellToolCancelReturnsQuickly(t *testing.T) {
	workspace := t.TempDir()
	shell := NewShellTool()
	ctx := workspacepath.WithWorkspaceStore(context.Background(), &testWorkspaceStore{dir: workspace})
	ctx, cancel := context.WithCancel(ctx)
	args, _ := json.Marshal(map[string]any{"cmd": "echo started > started.txt; sleep 5", "timeout_ms": 10000})
	done := make(chan error, 1)
	go func() {
		_, err := shell.Call(ctx, tool.CallRequest{Arguments: args})
		done <- err
	}()

	marker := filepath.Join(workspace, "started.txt")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		} else if !os.IsNotExist(err) {
			cancel()
			t.Fatalf("stat shell start marker: %v", err)
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("shell command did not start within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancelStarted := time.Now()
	cancel()
	select {
	case <-done:
		if elapsed := time.Since(cancelStarted); elapsed > time.Second {
			t.Fatalf("shell cancel took %s, want under 1s", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("shell cancel did not return within 1s")
	}
}

func TestShellToolRunsLS(t *testing.T) {

	shell := NewShellTool()
	args, _ := json.Marshal(map[string]any{"cmd": "ls", "timeout_ms": 5000})
	result, err := shell.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected shell result")
	}
}

func TestShellToolUsesConfiguredEnvironment(t *testing.T) {
	environment := processenv.New(os.Environ()).Overlay(map[string]string{"ELBOT_SHELL_ENV_TEST": "from-config"})
	shell := NewShellToolWithEnvironment(environment)
	cmdText := `printf %s "$ELBOT_SHELL_ENV_TEST"`
	if runtime.GOOS == "windows" {
		cmdText = `Write-Output $env:ELBOT_SHELL_ENV_TEST`
	}
	args, _ := json.Marshal(map[string]any{"cmd": cmdText})
	result, err := shell.Call(context.Background(), tool.CallRequest{Arguments: args})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(result.Content, "from-config") {
		t.Fatalf("shell result = %q", result.Content)
	}
}

func TestPowerShellUTF8Command(t *testing.T) {
	cmd := `Get-ChildItem -Name`
	got := powershellUTF8Command("pwsh", cmd)
	if !strings.HasPrefix(got, powershellUTF8Prelude) {
		t.Fatalf("expected PowerShell command to start with UTF-8 prelude, got %q", got)
	}
	if !strings.HasSuffix(got, cmd) {
		t.Fatalf("expected original command to be preserved, got %q", got)
	}
	if got := powershellUTF8Command(`C:\Program Files\PowerShell\7\pwsh.exe`, cmd); !strings.HasPrefix(got, powershellUTF8Prelude) {
		t.Fatalf("expected pwsh.exe path to use UTF-8 prelude, got %q", got)
	}
	if got := powershellUTF8Command("bash", cmd); got != cmd {
		t.Fatalf("bash command = %q, want %q", got, cmd)
	}
}

func TestResolveUnixShellPrefersBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix only")
	}
	name, args := resolveUnixShell()
	if _, err := exec.LookPath("bash"); err == nil {
		if name != "bash" {
			t.Fatalf("shell name = %q, want bash", name)
		}
	} else if name != "sh" {
		t.Fatalf("shell name = %q, want sh fallback", name)
	}
	wantArgs := []string{"-c"}
	if name == "bash" {
		wantArgs = []string{"--noprofile", "--norc", "-c"}
	}
	if strings.Join(args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("shell args = %v, want %v", args, wantArgs)
	}
}

func TestDetectWindowsShellPrefersPwshThenBash(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}

	root := t.TempDir()
	pwshDir := filepath.Join(root, "pwsh")
	bashDir := filepath.Join(root, "bash")
	emptyDir := filepath.Join(root, "empty")
	for _, dir := range []string{pwshDir, bashDir, emptyDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		filepath.Join(pwshDir, "pwsh.exe"),
		filepath.Join(bashDir, "bash.exe"),
	} {
		if err := os.WriteFile(path, []byte{}, 0755); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name     string
		path     string
		wantName string
		wantArgs []string
	}{
		{
			name:     "pwsh before bash",
			path:     pwshDir + string(os.PathListSeparator) + bashDir,
			wantName: "pwsh",
			wantArgs: []string{"-NoProfile", "-Command"},
		},
		{
			name:     "bash when pwsh missing",
			path:     bashDir,
			wantName: "bash",
			wantArgs: []string{"--noprofile", "--norc", "-c"},
		},
		{
			name:     "powershell fallback",
			path:     emptyDir,
			wantName: "powershell.exe",
			wantArgs: []string{"-NoProfile", "-Command"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PATH", tt.path)
			got := detectWindowsShell()
			if got.name != tt.wantName {
				t.Fatalf("shell name = %q, want %q", got.name, tt.wantName)
			}
			if strings.Join(got.args, "\x00") != strings.Join(tt.wantArgs, "\x00") {
				t.Fatalf("shell args = %v, want %v", got.args, tt.wantArgs)
			}
		})
	}
}

func TestResolveWindowsShellCachedAndValid(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	name1, args1 := resolveWindowsShell()
	name2, _ := resolveWindowsShell()
	if name1 != name2 {
		t.Fatalf("resolveWindowsShell not cached: %q vs %q", name1, name2)
	}
	detected := detectWindowsShell()
	if name1 != detected.name || strings.Join(args1, "\x00") != strings.Join(detected.args, "\x00") {
		t.Fatalf("resolved shell = %q %v, detected shell = %q %v", name1, args1, detected.name, detected.args)
	}
	if name1 == "" {
		t.Fatal("expected non-empty shell name")
	}
	if len(args1) == 0 {
		t.Fatal("expected non-empty shell args")
	}
	if name1 == "pwsh" || name1 == "powershell.exe" {
		if len(args1) < 2 || args1[0] != "-NoProfile" || args1[1] != "-Command" {
			t.Fatalf("unexpected powershell args: %v", args1)
		}
	}
	if name1 == "bash" {
		if len(args1) != 1 || args1[0] != "-lc" {
			t.Fatalf("unexpected bash args: %v", args1)
		}
	}
}
