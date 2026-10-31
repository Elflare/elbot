// 运行 Go Skill，处理执行环境与调用结果。
package skill

import toolshare "elbot/internal/tool/share"

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"elbot/internal/config"
	"elbot/internal/llm"
)

type GoRunner struct {
	manager *Manager // 借用 Skill 目录、媒体入口和进程环境。
}

type goRunnerArgs struct {
	SkillName string          `json:"skill_name"`
	TimeoutMS int             `json:"timeout_ms"`
	Payload   json.RawMessage `json:"-"`
}

func NewGoRunner(manager *Manager) GoRunner {
	return GoRunner{manager: manager}
}

func (GoRunner) Name() string { return GoRunnerName }

func (GoRunner) Info() toolshare.Info {
	return toolshare.NewBuilder(GoRunnerName).
		Description("运行Go Skill；stdin=payload JSON。").
		Source(toolshare.SourceBuiltin).
		Risk(toolshare.RiskLow).
		Hidden().
		BuildInfo()
}

func (r GoRunner) Schema() llm.ToolSchema {
	return toolshare.NewBuilder(GoRunnerName).
		Description(r.Info().Description).
		String("skill_name", "Go Skill名称。", toolshare.Required()).
		Object("payload", "业务参数；media_inputs=[{media:\"media:<sha256>\"}]→补可读path；[]→仅创建输出目录；其他字段原样传入。", toolshare.Required()).
		Integer("timeout_ms", "超时，ms；≤0/省略→30000。").
		BuildSchema()
}

func (r GoRunner) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	args, err := parseGoRunnerArgs(req.Arguments)
	if err != nil {
		return nil, fmt.Errorf("parse go_skill_run arguments: %w", err)
	}
	record, ok := r.manager.catalog.Get(strings.TrimSpace(args.SkillName))
	if !ok || record.Kind != KindGo || record.BinaryPath == "" {
		return nil, fmt.Errorf("go skill %q not found", args.SkillName)
	}
	preview := fmt.Sprintf("Go Skill：%s\n程序：%s\n工作目录：%s", record.Name, record.BinaryPath, record.Root)
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: record.Risk}, preview, func(ctx context.Context) (*toolshare.Result, string, error) {
		return r.call(ctx, args, record, runMediaCommand)
	}), nil
}

func (r GoRunner) call(ctx context.Context, args goRunnerArgs, record record, run func(context.Context, string, *exec.Cmd, toolshare.MediaCall) (*toolshare.Result, error)) (*toolshare.Result, string, error) {

	dotEnv, err := config.LoadEnvFile(filepath.Join(record.Root, ".env"))
	if err != nil {
		callErr := fmt.Errorf("load Go skill environment: %w", err)
		return nil, callErr.Error(), callErr
	}
	environment := r.manager.processEnv.Overlay(dotEnv)
	timeout := runnerTimeout(args.TimeoutMS)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	mediaCall := r.manager.media.NewCall()
	defer mediaCall.Close()
	payload, err := prepareGoMedia(runCtx, args.Payload, mediaCall, record.Root)
	if err != nil {
		{
			callErr := fmt.Errorf("prepare Go skill media: %w", err)
			return nil, callErr.Error(), callErr
		}
	}
	cmd := environment.CommandContext(runCtx, record.BinaryPath)
	cmd.Dir = record.Root
	cmd.Stdin = bytes.NewReader(payload)
	{
		result, callErr := run(runCtx, "go skill", cmd, mediaCall)
		return result, "", callErr
	}
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
	value := raw["payload"]
	if len(value) == 0 {
		return goRunnerArgs{}, fmt.Errorf("payload is required")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(value, &payload); err != nil {
		return goRunnerArgs{}, fmt.Errorf("payload must be object: %w", err)
	}
	args.Payload = json.RawMessage(value)
	return args, nil
}
