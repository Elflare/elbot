// 实现 Shell 工具，运行命令并收集输出。
package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/events"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"mvdan.cc/sh/v3/syntax"

	"elbot/internal/operationresult"
	"elbot/internal/processenv"
	"elbot/internal/workspace"
)

const (
	defaultShellTimeout   = 10 * time.Second
	shellCmdRequired      = `cmd is required; use {"cmd":"..."}`
	powershellUTF8Prelude = `$OutputEncoding = [System.Text.UTF8Encoding]::new($false); try { [Console]::OutputEncoding = $OutputEncoding } catch {}; `
)

type ShellTool struct {
	Media      interface{ NewCall() toolshare.MediaCall }
	FileGuard  FileGuard
	ProcessEnv processenv.Environment
}

type shellArgs struct {
	MediaInputs []toolshare.MediaInput `json:"media_inputs"`
	Cmd         string                 `json:"cmd"`
	TimeoutMS   int                    `json:"timeout_ms"`
}

type shellData struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func (t ShellTool) Definition() toolshare.ToolDefinition {
	builder := shellBuilder()

	schema := builder.BuildSchema()
	properties := schema.Parameters["properties"].(map[string]any)
	inputs := properties["media_inputs"].(map[string]any)
	inputs["items"].(map[string]any)["additionalProperties"] = false
	definition := builder.Build(t.PrepareCall)
	definition.Parameters = schema.Parameters

	return definition
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
	return toolshare.NewBuilder("shell").Async(toolshare.SyncByDefault).
		Description(fmt.Sprintf("执行命令，语法=%s。py用tool_weave", currentShellDesc())).
		Risk(toolshare.RiskHigh).
		SuperadminOnly().
		DependsOn("workspace", "draft").
		String("cmd", "命令。", toolshare.Required()).
		Integer("timeout_ms", "超时，ms；≤0/省略→10000。").
		Weavable().
		ObjectArray("media_inputs", "第i项媒体→路径环境变量ELBOT_MEDIA_i，i从1开始；按当前shell语法引用。", map[string]any{"media": map[string]any{"type": "string", "pattern": "^media:[0-9a-f]{64}$"}}, []string{"media"})
}

func (t ShellTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {

	args, cmdText, err := decodeShellArgs(req)
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}

	timeout := defaultShellTimeout
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
	}

	var file *syntax.File
	var parseErr error
	powerShell := isPowerShellEnv()
	if !powerShell {
		file, parseErr = syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(cmdText), "")
		if parseErr == nil {
			if err := rejectBashShellCommand(file); err != nil {
				return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Forbidden, Message: err.Error()}, nil
			}
		}
	}
	workDir, err := workspace.CurrentWorkspaceDir(ctx)
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{}, err
	}
	var advice shellAdvice
	assessment := toolshare.RiskAssessment{Level: toolshare.RiskHigh}
	switch {
	case powerShell:
		assessment.Reasons = []string{"PowerShell 环境无法用 bash AST 解析命令"}
	case parseErr != nil:
		assessment.Reasons = []string{"shell AST 解析失败，按高风险处理: " + parseErr.Error()}
	default:
		advice = analyzeBashShellAdvice(file, workDir, t.FileGuard)
		if advice.blocked.Status != operationresult.Success {
			return operationresult.Result[*toolshare.PreparedCall]{Status: advice.blocked.Status, Message: advice.blocked.Message}, nil
		}
		assessment = classifyBashShellCommand(file)
	}
	if scope, ok := workspace.PathScopeFromContext(ctx); ok && scope.Restricted && !powerShell {
		if parseErr != nil {
			assessment.Level = toolshare.RiskCritical
			assessment.Reasons = append(assessment.Reasons, "受限调用无法解析 shell AST: "+parseErr.Error())
		} else {
			assessment = applyBashShellSandboxRisk(file, assessment)
		}
	}
	details := []toolshare.ConfirmationDetailBlock{
		toolshare.CodeConfirmationDetail(currentShellDesc(), cmdText),
		toolshare.FieldsConfirmationDetail(
			toolshare.ConfirmationField{Key: "work_dir", Label: "工作目录", Value: workDir},
			toolshare.ConfirmationField{Key: "timeout_ms", Label: "超时（ms）", Value: timeout.Milliseconds()},
			toolshare.ConfirmationField{Key: "media_inputs", Label: "输入媒体", Value: args.MediaInputs},
		),
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, assessment, details, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		environment := t.ProcessEnv
		if len(args.MediaInputs) > 0 {
			mediaCall := t.Media.NewCall()
			defer mediaCall.Close()
			variables := make(map[string]string, len(args.MediaInputs))
			for i, input := range args.MediaInputs {
				path, err := mediaCall.CachedExport(runCtx, input.MediaID)
				if err != nil {
					callErr := fmt.Errorf("prepare shell media: %w", err)
					return operationresult.Result[*toolshare.Result]{}, callErr.Error(), callErr
				}
				if path.Status != operationresult.Success {
					return operationresult.Result[*toolshare.Result]{Status: path.Status, Message: path.Message}, path.Message, nil
				}
				variables[fmt.Sprintf("ELBOT_MEDIA_%d", i+1)] = path.Data
			}
			environment = environment.Overlay(variables)
		}
		cmd := shellCommand(runCtx, environment, cmdText)
		cmd.Dir = workDir
		var stdout, stderr toolshare.ProcessOutput
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		cancelCommand := cmd.Cancel
		cancelRequested := make(chan error, 1)
		cmd.Cancel = func() error {
			err := cancelCommand()
			cancelRequested <- err
			return err
		}
		err := cmd.Run()
		select {
		case cancelErr := <-cancelRequested:
			if cancelErr != nil {
				if !errors.Is(cancelErr, os.ErrProcessDone) && !errors.Is(err, cancelErr) {
					events.AsyncLog(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "shell_process_cleanup_failed", Module: "tool", Content: cancelErr.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(cancelErr)})
				}
			} else if processenv.ExitCanceled(cmd, true) {
				err = runCtx.Err()
			}
		default:
		}
		if errors.Is(err, context.DeadlineExceeded) {
			message := fmt.Sprintf("shell timed out: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Timeout, Message: message}, message, nil
		}
		if errors.Is(err, context.Canceled) {
			message := fmt.Sprintf("shell canceled: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Canceled, Message: message}, message, nil
		}
		exitCode := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			exitCode = exitErr.ExitCode()
		} else if err != nil {
			callErr := fmt.Errorf("run shell: %w", err)
			return operationresult.Result[*toolshare.Result]{}, callErr.Error(), callErr
		}
		data := shellData{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}
		content := formatShellContent(data)
		result := &toolshare.Result{Content: content, Warnings: advice.warnings}
		if exitCode != 0 {
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Failed, Message: content}, fmt.Sprintf("exit_code=%d", exitCode), nil
		}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, fmt.Sprintf("exit_code=%d", exitCode), nil
	})}, nil
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

func rejectBashShellCommand(file *syntax.File) error {
	var blocked error
	syntax.Walk(file, func(node syntax.Node) bool {
		if blocked != nil {
			return false
		}
		stmt, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}
		call, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		name, ok := literalWord(call.Args[0])
		if !ok {
			return true
		}
		switch commandBase(name) {
		case "cd", "chdir", "pushd", "popd", "set-location", "sl":
			blocked = fmt.Errorf("shell command %q is not allowed; use the workspace tool to set the working directory", commandBase(name))
		}
		if name != "python3" {
			return true
		}
		const message = "shell禁止py，用tool_weave"
		if len(call.Args) > 1 {
			arg, _ := literalWord(call.Args[1])
			if arg == "-c" && len(call.Args) >= 3 {
				blocked = errors.New(message)
			}
			if arg != "-" || len(call.Args) != 2 {
				return true
			}
		}
		for _, r := range stmt.Redirs {
			if delimiter, _ := literalWord(r.Word); r.Op == syntax.Hdoc && (r.N == nil || r.N.Value == "0") && delimiter == "PY" {
				blocked = errors.New(message)
				return false
			}
		}
		return true
	})
	return blocked
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

func isPowerShellEnv() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	name, _ := resolveWindowsShell()
	return name == "pwsh" || name == "powershell.exe"
}
