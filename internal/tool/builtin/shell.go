package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"elbot/internal/llm"
	"elbot/internal/tool"
)

const (
	defaultShellTimeout = 10 * time.Second
	maxShellOutput      = 16 * 1024
	shellCmdRequired    = `cmd is required; use {"cmd":"..."}`
)

type ShellTool struct{}

type shellArgs struct {
	Cmd       string `json:"cmd"`
	TimeoutMS int    `json:"timeout_ms"`
}

type shellData struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func NewShellTool() ShellTool {
	return ShellTool{}
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
		Integer("timeout_ms", "可选，命令超时时间，默认 10000。")
}

func (t ShellTool) AssessRisk(ctx context.Context, req tool.CallRequest) (tool.RiskAssessment, error) {
	var args shellArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return tool.RiskAssessment{}, fmt.Errorf("parse shell arguments: %w", err)
		}
	}
	cmdText := strings.TrimSpace(args.Cmd)
	if cmdText == "" {
		return tool.RiskAssessment{}, fmt.Errorf(shellCmdRequired)
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

func (t ShellTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args shellArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse shell arguments: %w", err)
		}
	}
	cmdText := strings.TrimSpace(args.Cmd)
	if cmdText == "" {
		return nil, fmt.Errorf(shellCmdRequired)
	}

	timeout := defaultShellTimeout
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := shellCommand(runCtx, cmdText)
	configureShellProcess(cmd)
	if sandbox, ok := tool.SandboxContextFromContext(ctx); ok && strings.TrimSpace(sandbox.Dir) != "" {
		if err := os.MkdirAll(sandbox.Dir, 0755); err != nil {
			return nil, fmt.Errorf("create shell sandbox: %w", err)
		}
		cmd.Dir = sandbox.Dir
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := runShellCommand(runCtx, cmd)
	data := shellData{Stdout: truncate(stdout.String()), Stderr: truncate(stderr.String())}
	if exitErr, ok := err.(*exec.ExitError); ok {
		data.ExitCode = exitErr.ExitCode()
	} else if err != nil {
		return nil, fmt.Errorf("run shell: %w", err)
	}
	return &tool.Result{Content: formatShellContent(data)}, nil
}

func shellCommand(ctx context.Context, cmdText string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "bash", "-lc", cmdText)
	}
	return exec.CommandContext(ctx, "sh", "-lc", cmdText)
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
		killShellProcessTree(cmd)
		err := <-errCh
		if err != nil {
			return err
		}
		return ctx.Err()
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
