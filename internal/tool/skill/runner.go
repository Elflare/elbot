package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"elbot/internal/llm"
	"elbot/internal/tool"
)

const (
	defaultRunnerTimeout = 30 * time.Second
	maxRunnerOutput      = 16 * 1024
)

type PythonRunner struct {
	Catalog *Catalog
}

type pythonRunnerArgs struct {
	Skill     string   `json:"skill"`
	Script    string   `json:"script"`
	Args      []string `json:"args"`
	Stdin     string   `json:"stdin"`
	TimeoutMS int      `json:"timeout_ms"`
}

func NewPythonRunner(catalog *Catalog) PythonRunner {
	return PythonRunner{Catalog: catalog}
}

func (PythonRunner) Name() string { return PythonRunnerName }

func (PythonRunner) Info() tool.Info {
	return tool.NewBuilder(PythonRunnerName).
		Description("在指定 Python skill 目录内用 uv run python 执行脚本。").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskLow).
		Hidden().
		BuildInfo()
}

func (PythonRunner) Schema() llm.ToolSchema {
	return tool.NewBuilder(PythonRunnerName).
		Description("在指定 Python skill 目录内用 uv run python 执行脚本。").
		String("skill", "Python skill 名称。", tool.Required()).
		String("script", "相对 skill 目录的 Python 脚本路径，例如 scripts/accept_changes.py。", tool.Required()).
		StringArray("args", "传给脚本的命令行参数数组。").
		String("stdin", "可选，写入脚本标准输入的文本。").
		Integer("timeout_ms", "可选，超时时间，默认 30000。").
		BuildSchema()
}

func (r PythonRunner) AssessRisk(ctx context.Context, req tool.CallRequest) (tool.RiskAssessment, error) {
	_ = ctx
	var args pythonRunnerArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return tool.RiskAssessment{}, fmt.Errorf("parse python_skill_run arguments: %w", err)
		}
	}
	if r.Catalog == nil {
		return tool.RiskAssessment{Level: tool.RiskHigh}, nil
	}
	record, ok := r.Catalog.Get(strings.TrimSpace(args.Skill))
	if !ok || record.Kind != KindPython {
		return tool.RiskAssessment{Level: tool.RiskHigh}, nil
	}
	return tool.RiskAssessment{Level: record.Risk}, nil
}

func (r PythonRunner) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args pythonRunnerArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, fmt.Errorf("parse python_skill_run arguments: %w", err)
		}
	}
	if r.Catalog == nil {
		return nil, fmt.Errorf("skill catalog is not configured")
	}
	record, ok := r.Catalog.Get(strings.TrimSpace(args.Skill))
	if !ok || record.Kind != KindPython {
		return nil, fmt.Errorf("python skill %q not found", args.Skill)
	}
	script, err := safeRelativePath(record.Root, args.Script)
	if err != nil {
		return nil, err
	}
	timeout := runnerTimeout(args.TimeoutMS)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "uv", append([]string{"run", "python", script}, args.Args...)...)
	cmd.Dir = record.Root
	if args.Stdin != "" {
		cmd.Stdin = strings.NewReader(args.Stdin)
	}
	return runCommand(cmd)
}

type GoRunner struct {
	Catalog *Catalog
}

type goRunnerArgs struct {
	SkillName string          `json:"skill_name"`
	TimeoutMS int             `json:"timeout_ms"`
	Payload   json.RawMessage `json:"-"`
}

func NewGoRunner(catalog *Catalog) GoRunner {
	return GoRunner{Catalog: catalog}
}

func (GoRunner) Name() string { return GoRunnerName }

func (GoRunner) Info() tool.Info {
	return tool.NewBuilder(GoRunnerName).
		Description("运行指定 Go skill，并把 arguments JSON 写入 stdin。").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskLow).
		Hidden().
		BuildInfo()
}

func (GoRunner) Schema() llm.ToolSchema {
	return tool.NewBuilder(GoRunnerName).
		Description("运行指定 Go skill，并把 payload JSON 原样写入 skill stdin。调用 Go skill 时必须通过本 runner：skill_name 选择 skill，payload 放业务参数对象，timeout_ms 设置超时。兼容旧格式：未提供 payload 时，除 skill_name/timeout_ms 外的顶层字段会作为业务参数 JSON 写入 stdin。").
		String("skill_name", "Go skill 名称。", tool.Required()).
		Object("payload", "业务参数 JSON 对象，会原样写入 Go skill 的 stdin。例如 {\"url\":\"magnet:?xt=...\",\"title\":\"番剧名\"}。", tool.Required()).
		Integer("timeout_ms", "可选，超时时间，默认 30000。").
		BuildSchema()
}

func (r GoRunner) AssessRisk(ctx context.Context, req tool.CallRequest) (tool.RiskAssessment, error) {
	_ = ctx
	var args goRunnerArgs
	if len(req.Arguments) > 0 {
		var err error
		args, err = parseGoRunnerArgs(req.Arguments)
		if err != nil {
			return tool.RiskAssessment{}, fmt.Errorf("parse go_skill_run arguments: %w", err)
		}
	}
	if r.Catalog == nil {
		return tool.RiskAssessment{Level: tool.RiskHigh}, nil
	}
	record, ok := r.Catalog.Get(strings.TrimSpace(args.SkillName))
	if !ok || record.Kind != KindGo {
		return tool.RiskAssessment{Level: tool.RiskHigh}, nil
	}
	return tool.RiskAssessment{Level: record.Risk}, nil
}

func (r GoRunner) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args goRunnerArgs
	if len(req.Arguments) > 0 {
		var err error
		args, err = parseGoRunnerArgs(req.Arguments)
		if err != nil {
			return nil, fmt.Errorf("parse go_skill_run arguments: %w", err)
		}
	}
	if r.Catalog == nil {
		return nil, fmt.Errorf("skill catalog is not configured")
	}
	record, ok := r.Catalog.Get(strings.TrimSpace(args.SkillName))
	if !ok || record.Kind != KindGo || record.BinaryPath == "" {
		return nil, fmt.Errorf("go skill %q not found", args.SkillName)
	}
	if len(args.Payload) == 0 {
		args.Payload = json.RawMessage(`{}`)
	}
	timeout := runnerTimeout(args.TimeoutMS)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, record.BinaryPath)
	cmd.Dir = record.Root
	cmd.Stdin = bytes.NewReader(args.Payload)
	return runCommand(cmd)
}

func parseGoRunnerArgs(data json.RawMessage) (goRunnerArgs, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return goRunnerArgs{}, err
	}
	var args goRunnerArgs
	if value := raw["skill_name"]; len(value) > 0 {
		if err := json.Unmarshal(value, &args.SkillName); err != nil {
			return goRunnerArgs{}, fmt.Errorf("skill_name: %w", err)
		}
	}
	if value := raw["timeout_ms"]; len(value) > 0 {
		if err := json.Unmarshal(value, &args.TimeoutMS); err != nil {
			return goRunnerArgs{}, fmt.Errorf("timeout_ms: %w", err)
		}
	}
	if value := raw["payload"]; len(value) > 0 {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(value, &payload); err != nil {
			return goRunnerArgs{}, fmt.Errorf("payload must be object: %w", err)
		}
		args.Payload = json.RawMessage(value)
		return args, nil
	}
	// Backward compatibility: older calls may put business fields at top level.
	delete(raw, "skill_name")
	delete(raw, "timeout_ms")
	payload, err := json.Marshal(raw)
	if err != nil {
		return goRunnerArgs{}, fmt.Errorf("payload: %w", err)
	}
	args.Payload = payload
	return args, nil
}

func runnerTimeout(timeoutMS int) time.Duration {
	if timeoutMS > 0 {
		return time.Duration(timeoutMS) * time.Millisecond
	}
	return defaultRunnerTimeout
}

func safeRelativePath(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("script is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("script must be relative to skill directory")
	}
	clean := filepath.Clean(rel)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("script must stay inside skill directory")
	}
	full := filepath.Join(root, clean)
	back, err := filepath.Rel(root, full)
	if err != nil || strings.HasPrefix(back, "..") || filepath.IsAbs(back) {
		return "", fmt.Errorf("script must stay inside skill directory")
	}
	return clean, nil
}

func runCommand(cmd *exec.Cmd) (*tool.Result, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := truncateOutput(stdout.String())
	errText := truncateOutput(stderr.String())
	if err != nil {
		if errText != "" {
			return nil, fmt.Errorf("run command: %w: %s", err, errText)
		}
		return nil, fmt.Errorf("run command: %w", err)
	}
	return resultFromStdout(out)
}

func resultFromStdout(out string) (*tool.Result, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return &tool.Result{Content: ""}, nil
	}
	var structured struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(trimmed), &structured); err == nil && structured.Content != "" {
		return &tool.Result{Content: structured.Content}, nil
	}
	return &tool.Result{Content: out}, nil
}

func truncateOutput(text string) string {
	if len(text) <= maxRunnerOutput {
		return text
	}
	return text[:maxRunnerOutput] + "\n... output truncated ...\n"
}
