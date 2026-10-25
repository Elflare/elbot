// 实现 Shell 工具，运行命令并收集输出。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"mvdan.cc/sh/v3/syntax"

	"elbot/internal/llm"
	"elbot/internal/processenv"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/tool"
	"elbot/internal/workspace"
)

const (
	defaultShellTimeout   = 10 * time.Second
	maxShellOutput        = 256 * 1024
	shellCmdRequired      = `cmd is required; use {"cmd":"..."}`
	warnUseWorkspace      = "需要切换工作目录时请使用 workspace 工具，不要在 cmd 中切换目录或夹带目录切换。"
	powershellUTF8Prelude = `$OutputEncoding = [System.Text.UTF8Encoding]::new($false); try { [Console]::OutputEncoding = $OutputEncoding } catch {}; `
)

type ShellTool struct {
	Media      *tool.MediaRuntime
	FileGuard  *FileGuard
	ProcessEnv processenv.Environment
}

type shellArgs struct {
	MediaInputs []tool.MediaInput `json:"media_inputs"`
	Cmd         string            `json:"cmd"`
	TimeoutMS   int               `json:"timeout_ms"`
}

type shellData struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func NewShellTool(fileGuard ...*FileGuard) ShellTool {
	return NewShellToolWithEnvironment(processenv.Environment{}, fileGuard...)
}

func NewShellToolWithEnvironment(environment processenv.Environment, fileGuard ...*FileGuard) ShellTool {
	return ShellTool{FileGuard: firstFileGuard(fileGuard), ProcessEnv: environment}
}

func (ShellTool) Name() string {
	return "shell"
}

func (t ShellTool) Info() toolshare.Info {
	return shellBuilder().BuildInfo()
}

func (t ShellTool) Schema() llm.ToolSchema {
	schema := shellBuilder().BuildSchema()
	properties := schema.Parameters["properties"].(map[string]any)
	inputs := properties["media_inputs"].(map[string]any)
	inputs["items"].(map[string]any)["additionalProperties"] = false
	return schema
}

func currentShellDesc() string {
	if runtime.GOOS != "windows" {
		name, _ := resolveUnixShell()
		if name == "bash" {
			return "bash"
		}
		return "sh (POSIX shell)"
	}
	name, _ := resolveWindowsShell()
	switch name {
	case "bash":
		return "bash"
	case "pwsh":
		return "PowerShell (pwsh)"
	default:
		return "Windows PowerShell"
	}
}

func shellBuilder() *toolshare.Builder {
	return toolshare.NewBuilder("shell").
		Description(fmt.Sprintf("执行 shell 命令。当前使用 %s，请使用相应语法。", currentShellDesc())).
		Risk(toolshare.RiskHigh).
		DependsOn("workspace").
		Tags("agent").
		String("cmd", "要执行的 shell 命令。", toolshare.Required()).
		Integer("timeout_ms", "可选，命令超时时间，默认 10000。").
		ObjectArray("media_inputs", "可选，显式媒体输入；宿主导出并注入 ELBOT_MEDIA_1 等环境变量，命令需按当前 shell 语法引用变量。", map[string]any{"media": map[string]any{"type": "string", "pattern": "^media:[0-9a-f]{64}$"}}, []string{"media"})
}

func (t ShellTool) AssessRisk(ctx context.Context, req toolshare.CallRequest) (toolshare.RiskAssessment, error) {
	_, cmdText, err := decodeShellArgs(req)
	if err != nil {
		return toolshare.RiskAssessment{}, err
	}
	assessment := classifyShellCommand(cmdText)
	if sandbox, ok := sandboxctx.SandboxContextFromContext(ctx); ok && sandbox.Background {
		assessment = applyShellSandboxRisk(cmdText, assessment)
	}
	if assessment.Level == "" {
		assessment.Level = t.Info().Risk
	}
	return assessment, nil
}

func (t ShellTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	_, cmdText, err := decodeShellArgs(req)
	if err != nil {
		return nil, err
	}
	if err := rejectShellDirectoryChange(cmdText); err != nil {
		return nil, err
	}
	workDir, err := resolveShellWorkDir(ctx)
	if err != nil {
		return nil, err
	}
	return nil, analyzeShellAdvice(cmdText, workDir, t.FileGuard).blockErr
}

func (t ShellTool) Call(ctx context.Context, req toolshare.CallRequest) (result *toolshare.Result, logContent string, callErr error) {
	defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()

	args, cmdText, err := decodeShellArgs(req)
	if err != nil {
		return nil, err.Error(), err
	}

	timeout := defaultShellTimeout
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := rejectShellDirectoryChange(cmdText); err != nil {
		return nil, err.Error(), err
	}
	workDir, err := resolveShellWorkDir(ctx)
	if err != nil {
		return nil, err.Error(), err
	}
	advice := analyzeShellAdvice(cmdText, workDir, t.FileGuard)
	if advice.blockErr != nil {
		return nil, advice.blockErr.Error(), advice.blockErr
	}
	environment := t.ProcessEnv
	if len(args.MediaInputs) > 0 {
		mediaCall := t.Media.NewCall()
		defer func() {
			err := mediaCall.Close()
			callErr = errors.Join(callErr, err)
			if err != nil {
				logContent = fmt.Sprintf("media_cleanup_error=%v\n%s", err, logContent)
			}
		}()
		variables := make(map[string]string, len(args.MediaInputs))
		for i, input := range args.MediaInputs {
			path, err := mediaCall.CachedExport(runCtx, input.MediaID)
			if err != nil {
				{
					callErr := fmt.Errorf("prepare shell media: %w", err)
					return nil, callErr.Error(), callErr
				}
			}
			variables[fmt.Sprintf("ELBOT_MEDIA_%d", i+1)] = path
		}
		environment = environment.Overlay(variables)
	}
	cmd := shellCommand(runCtx, environment, cmdText)
	cmd.Dir = workDir
	var stdout shellOutputBuffer
	var stderr shellOutputBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	exitCode := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	} else if err != nil {
		{
			callErr := fmt.Errorf("run shell: %w", err)
			return nil, callErr.Error(), callErr
		}
	}
	data := shellData{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}
	logContent = fmt.Sprintf("exit_code=%d", exitCode)
	if stderr := strings.TrimRight(data.Stderr, "\r\n"); stderr != "" {
		logContent += "\nstderr:\n" + stderr
	}
	if stdout := strings.TrimRight(data.Stdout, "\r\n"); stdout != "" {
		logContent += "\nstdout:\n" + stdout
	}
	{
		result := &toolshare.Result{Content: formatShellContent(data), Warnings: advice.warnings}
		return result, logContent, nil
	}
}

func decodeShellArgs(req toolshare.CallRequest) (shellArgs, string, error) {
	var args shellArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return shellArgs{}, "", fmt.Errorf("parse shell arguments: %w", err)
		}
	}
	cmdText := strings.TrimSpace(args.Cmd)
	if cmdText == "" {
		return shellArgs{}, "", fmt.Errorf(shellCmdRequired)
	}
	return args, cmdText, nil
}

func resolveShellWorkDir(ctx context.Context) (string, error) {
	if sandbox, ok := sandboxctx.SandboxContextFromContext(ctx); ok && strings.TrimSpace(sandbox.Dir) != "" {
		if err := os.MkdirAll(sandbox.Dir, 0755); err != nil {
			return "", fmt.Errorf("create shell sandbox: %w", err)
		}
		return filepath.Clean(sandbox.Dir), nil
	}
	return workspace.CurrentWorkspaceDir(ctx)
}

func rejectShellDirectoryChange(cmdText string) error {
	if isPowerShellEnv() {
		return nil
	}
	return rejectBashShellDirectoryChange(cmdText)
}

func rejectBashShellDirectoryChange(cmdText string) error {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(cmdText), "")
	if err != nil {
		return nil
	}
	var blocked string
	syntax.Walk(file, func(node syntax.Node) bool {
		if blocked != "" {
			return false
		}
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		name, ok := literalWord(call.Args[0])
		if !ok {
			return true
		}
		switch commandBase(name) {
		case "cd", "chdir", "pushd", "popd", "set-location", "sl":
			blocked = commandBase(name)
		}
		return true
	})
	if blocked != "" {
		return fmt.Errorf("shell command %q is not allowed; use the workspace tool to set the working directory", blocked)
	}
	return nil
}

func shellCommand(ctx context.Context, environment processenv.Environment, cmdText string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		name, args := resolveWindowsShell()
		return environment.CommandContext(ctx, name, append(args, powershellUTF8Command(name, cmdText))...)
	}
	name, args := resolveUnixShell()
	return environment.CommandContext(ctx, name, append(args, cmdText)...)
}

func powershellUTF8Command(shellName, cmdText string) string {
	if !isPowerShellShell(shellName) {
		return cmdText
	}
	return powershellUTF8Prelude + cmdText
}

func isPowerShellShell(shellName string) bool {
	shellBase := filepath.Base(strings.ReplaceAll(shellName, `\`, "/"))
	switch strings.ToLower(shellBase) {
	case "pwsh", "pwsh.exe", "powershell", "powershell.exe":
		return true
	default:
		return false
	}
}

type windowsShell struct {
	name string
	args []string
}

var (
	windowsShellOnce     sync.Once
	windowsShellResolved windowsShell
)

func resolveWindowsShell() (string, []string) {
	windowsShellOnce.Do(func() {
		windowsShellResolved = detectWindowsShell()
	})
	return windowsShellResolved.name, windowsShellResolved.args
}

func detectWindowsShell() windowsShell {
	if _, err := exec.LookPath("pwsh"); err == nil {
		return windowsShell{name: "pwsh", args: []string{"-NoProfile", "-Command"}}
	}
	if _, err := exec.LookPath("bash"); err == nil {
		return windowsShell{name: "bash", args: []string{"--noprofile", "--norc", "-c"}}
	}
	return windowsShell{name: "powershell.exe", args: []string{"-NoProfile", "-Command"}}
}

func resolveUnixShell() (string, []string) {
	if _, err := exec.LookPath("bash"); err == nil {
		return "bash", []string{"--noprofile", "--norc", "-c"}
	}
	return "sh", []string{"-c"}
}

func formatShellContent(data shellData) string {
	parts := []string{}
	if data.Stdout != "" {
		parts = append(parts, data.Stdout)
	}
	if data.Stderr != "" {
		parts = append(parts, "stderr:\n"+data.Stderr)
	}
	if data.ExitCode != 0 {
		parts = append(parts, fmt.Sprintf("exit_code: %d", data.ExitCode))
	}
	return strings.Join(parts, "\n")
}

// shellOutputBuffer retains a bounded prefix while draining all process output.
type shellOutputBuffer struct {
	data      []byte
	truncated bool
}

func (b *shellOutputBuffer) Write(p []byte) (int, error) {
	keep := min(len(p), maxShellOutput-len(b.data))
	if keep > 0 {
		if b.data == nil {
			b.data = make([]byte, 0, maxShellOutput)
		}
		b.data = append(b.data, p[:keep]...)
	}
	b.truncated = b.truncated || keep < len(p)
	return len(p), nil
}

func (b *shellOutputBuffer) String() string {
	text := string(b.data)
	if b.truncated {
		text += fmt.Sprintf("\n... output too long; truncated to first %d KiB ...\n", maxShellOutput/1024)
	}
	return text
}

func isPowerShellEnv() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	name, _ := resolveWindowsShell()
	return name == "pwsh" || name == "powershell.exe"
}
