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
	defaultShellTimeout = 10 * time.Second
	maxShellOutput      = 16 * 1024
	shellCmdRequired    = `cmd is required; use {"cmd":"..."}`
	shellPathRemembered = "working directory set to %s; future shell calls in this session can omit path."
	warnUseShellPath    = "需要切换工作目录时请使用 shell 的 path 参数，不要在 cmd 中切换目录或夹带目录切换。"
)

type ShellTool struct {
	FileGuard *FileGuard
}

type shellArgs struct {
	Cmd       string `json:"cmd"`
	Path      string `json:"path"`
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

func shellBuilder() *tool.Builder {
	return tool.NewBuilder("shell").
		Description("执行 shell 命令。命令会按当前平台通过系统 shell 运行。").
		Risk(tool.RiskHigh).
		Tags("agent").
		String("cmd", "要执行的 shell 命令。", tool.Required()).
		String("path", "可选，命令工作目录；传入后本次 shell 会在该目录执行。").
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
	args, cmdText, err := decodeShellArgs(req)
	if err != nil {
		return err
	}
	if err := rejectShellDirectoryChange(cmdText); err != nil {
		return err
	}
	workDir, _, err := resolveShellWorkDir(ctx, args.Path)
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
	workDir, explicitPath, err := resolveShellWorkDir(ctx, args.Path)
	if err != nil {
		return nil, err
	}
	advice := analyzeShellAdvice(cmdText, workDir, t.FileGuard)
	if advice.blockErr != nil {
		return nil, advice.blockErr
	}
	if explicitPath && !shellSandboxContext(ctx) {
		advice.addWarning(fmt.Sprintf(shellPathRemembered, workDir))
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
	if explicitPath && !shellSandboxContext(ctx) {
		if store, ok := tool.ShellCWDStoreFromContext(ctx); ok {
			if err := store.SetShellCWD(ctx, workDir); err != nil {
				advice.addWarning("failed to persist shell working directory: " + err.Error())
			}
		}
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

func resolveShellWorkDir(ctx context.Context, rawPath string) (string, bool, error) {
	rawPath = strings.TrimSpace(rawPath)
	if sandbox, ok := tool.SandboxContextFromContext(ctx); ok && strings.TrimSpace(sandbox.Dir) != "" {
		return resolveSandboxShellWorkDir(sandbox, rawPath)
	}
	if rawPath != "" {
		workDir, err := resolveForegroundShellPath(rawPath)
		return workDir, true, err
	}
	if store, ok := tool.ShellCWDStoreFromContext(ctx); ok {
		stored, err := store.GetShellCWD(ctx)
		if err != nil {
			return "", false, fmt.Errorf("load shell working directory: %w", err)
		}
		if strings.TrimSpace(stored) != "" {
			workDir, err := validateShellWorkDir(stored)
			if err == nil {
				return workDir, false, nil
			}
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", false, fmt.Errorf("resolve shell workdir: %w", err)
	}
	return filepath.Clean(cwd), false, nil
}

// resolveSandboxShellWorkDir keeps background shell paths inside the current task sandbox dir.
// Each cron/Elnis task provides its own sandbox.Dir; raw path is a subdirectory under that dir.
func resolveSandboxShellWorkDir(sandbox tool.SandboxContext, rawPath string) (string, bool, error) {
	if rawPath == "" {
		if err := os.MkdirAll(sandbox.Dir, 0755); err != nil {
			return "", false, fmt.Errorf("create shell sandbox: %w", err)
		}
		return filepath.Clean(sandbox.Dir), false, nil
	}
	workDir, err := tool.ResolveSandboxRelativePath(sandbox, rawPath)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", false, fmt.Errorf("create shell sandbox path: %w", err)
	}
	return filepath.Clean(workDir), true, nil
}

// resolveForegroundShellPath resolves front-end shell path from process cwd or an absolute path.
// It intentionally does not resolve relative to the previously remembered session cwd.
func resolveForegroundShellPath(rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", fmt.Errorf("shell path is required")
	}
	if expanded, ok := msysPathToWindows(path); ok {
		path = expanded
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve shell workdir: %w", err)
		}
		path = filepath.Join(cwd, path)
	}
	return validateShellWorkDir(path)
}

func validateShellWorkDir(path string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return "", fmt.Errorf("shell path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("resolve shell path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("shell path is not a directory: %s", path)
	}
	return path, nil
}

func shellSandboxContext(ctx context.Context) bool {
	sandbox, ok := tool.SandboxContextFromContext(ctx)
	return ok && strings.TrimSpace(sandbox.Dir) != ""
}

func rejectShellDirectoryChange(cmdText string) error {
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
		return fmt.Errorf("shell command %q is not allowed; use the path argument to set the working directory", blocked)
	}
	return nil
}

func shellCommand(ctx context.Context, cmdText string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		name, args := resolveWindowsShell()
		return exec.CommandContext(ctx, name, append(args, cmdText)...)
	}
	return exec.CommandContext(ctx, "sh", "-lc", cmdText)
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
