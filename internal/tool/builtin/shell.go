package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"elbot/internal/llm"
	"elbot/internal/tool"
	"mvdan.cc/sh/v3/syntax"
)

const (
	defaultShellTimeout   = 10 * time.Second
	maxShellOutput        = 16 * 1024
	shellCmdRequired      = `cmd is required; use {"cmd":"..."}`
	warnUseWorkspace      = "需要切换工作目录时请使用 workspace 工具，不要在 cmd 中切换目录或夹带目录切换。"
	powershellUTF8Prelude = `$OutputEncoding = [System.Text.UTF8Encoding]::new($false); try { [Console]::OutputEncoding = $OutputEncoding } catch {}; `
)

type ShellTool struct {
	FileGuard *FileGuard
}

type shellArgs struct {
	Cmd       string `json:"cmd"`
	TimeoutMS int    `json:"timeout_ms"`
}

type shellData struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func NewShellTool(fileGuard ...*FileGuard) ShellTool {
	return ShellTool{FileGuard: firstFileGuard(fileGuard)}
}

func (ShellTool) Name() string {
	return "shell"
}

func (t ShellTool) Info() tool.Info {
	return shellBuilder().BuildInfo()
}

func (t ShellTool) Schema() llm.ToolSchema {
	return shellBuilder().BuildSchema()
}

func currentShellDesc() string {
	if runtime.GOOS != "windows" {
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

func shellBuilder() *tool.Builder {
	return tool.NewBuilder("shell").
		Description(fmt.Sprintf("执行 shell 命令。当前使用 %s，请使用相应语法。", currentShellDesc())).
		Risk(tool.RiskHigh).
		DependsOn("workspace").
		Tags("agent").
		String("cmd", "要执行的 shell 命令。", tool.Required()).
		Integer("timeout_ms", "可选，命令超时时间，默认 10000。")
}

func (t ShellTool) AssessRisk(ctx context.Context, req tool.CallRequest) (tool.RiskAssessment, error) {
	_, cmdText, err := decodeShellArgs(req)
	if err != nil {
		return tool.RiskAssessment{}, err
	}
	assessment := classifyShellCommand(cmdText)
	if sandbox, ok := tool.SandboxContextFromContext(ctx); ok && sandbox.Background {
		assessment = applyShellSandboxRisk(cmdText, assessment)
	}
	if assessment.Level == "" {
		assessment.Level = t.Info().Risk
	}
	return assessment, nil
}

func (t ShellTool) PreflightConfirmation(ctx context.Context, req tool.CallRequest) error {
	_, cmdText, err := decodeShellArgs(req)
	if err != nil {
		return err
	}
	if err := rejectShellDirectoryChange(cmdText); err != nil {
		return err
	}
	workDir, err := resolveShellWorkDir(ctx)
	if err != nil {
		return err
	}
	return analyzeShellAdvice(cmdText, workDir, t.FileGuard).blockErr
}

func (t ShellTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	args, cmdText, err := decodeShellArgs(req)
	if err != nil {
		return nil, err
	}

	timeout := defaultShellTimeout
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := rejectShellDirectoryChange(cmdText); err != nil {
		return nil, err
	}
	workDir, err := resolveShellWorkDir(ctx)
	if err != nil {
		return nil, err
	}
	advice := analyzeShellAdvice(cmdText, workDir, t.FileGuard)
	if advice.blockErr != nil {
		return nil, advice.blockErr
	}
	cmd := shellCommand(runCtx, cmdText)
	configureShellProcess(cmd)
	cmd.Dir = workDir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = runShellCommand(runCtx, cmd)
	exitCode := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	} else if err != nil {
		return nil, fmt.Errorf("run shell: %w", err)
	}
	data := shellData{Stdout: truncate(stdout.String()), Stderr: truncate(stderr.String()), ExitCode: exitCode}
	return &tool.Result{Content: formatShellContent(data), Warnings: advice.warnings}, nil
}

func decodeShellArgs(req tool.CallRequest) (shellArgs, string, error) {
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
	if sandbox, ok := tool.SandboxContextFromContext(ctx); ok && strings.TrimSpace(sandbox.Dir) != "" {
		if err := os.MkdirAll(sandbox.Dir, 0755); err != nil {
			return "", fmt.Errorf("create shell sandbox: %w", err)
		}
		return filepath.Clean(sandbox.Dir), nil
	}
	return tool.CurrentWorkspaceDir(ctx)
}

func rejectShellDirectoryChange(cmdText string) error {
	if isPowerShellEnv() {
		return nil
	}
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

func shellCommand(ctx context.Context, cmdText string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		name, args := resolveWindowsShell()
		return exec.CommandContext(ctx, name, append(args, powershellUTF8Command(name, cmdText))...)
	}
	return exec.CommandContext(ctx, "sh", "-lc", cmdText)
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
	if _, err := exec.LookPath("bash"); err == nil {
		return windowsShell{name: "bash", args: []string{"-lc"}}
	}
	if _, err := exec.LookPath("pwsh"); err == nil {
		return windowsShell{name: "pwsh", args: []string{"-NoProfile", "-Command"}}
	}
	return windowsShell{name: "powershell.exe", args: []string{"-NoProfile", "-Command"}}
}

func runShellCommand(ctx context.Context, cmd *exec.Cmd) error {
	errCh := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { errCh <- cmd.Wait() }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		go killShellProcessTree(cmd)
		select {
		case err := <-errCh:
			if err != nil {
				return err
			}
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
			return ctx.Err()
		}
	}
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

func truncate(text string) string {
	if len(text) <= maxShellOutput {
		return text
	}
	return text[:maxShellOutput] + "\n... output truncated ...\n"
}

func isPowerShellEnv() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	name, _ := resolveWindowsShell()
	return name == "pwsh" || name == "powershell.exe"
}
