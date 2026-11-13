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
	"elbot/internal/operationresult"
)

type GoRunner struct {
	manager *Manager // 借用 Skill 目录、媒体入口和进程环境。
}

type goRunnerArgs struct {
	SkillName string          `json:"skill_name"`
	TimeoutMS int             `json:"timeout_ms"`
	Payload   json.RawMessage `json:"-"`
}

func (t GoRunner) Definition() toolshare.ToolDefinition {
	builder := toolshare.NewBuilder(GoRunnerName).
		Description("运行Go Skill；stdin=payload JSON。").
		Risk(toolshare.RiskLow).
		Hidden()

	definition := builder.
		String("skill_name", "Go Skill名称。", toolshare.Required()).
		Object("payload", "业务参数；media_inputs=[{media:\"media:<sha256>\"}]→补可读path；[]→仅创建输出目录；其他字段原样传入。", toolshare.Required()).
		Integer("timeout_ms", "超时，ms；≤0/省略→30000。").Build(t.PrepareCall)
	return definition
}

func (r GoRunner) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	args, err := parseGoRunnerArgs(req.Arguments)
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("parse go_skill_run arguments: %v", err)}, nil
	}
	record, ok := r.manager.catalog.Get(strings.TrimSpace(args.SkillName))
	if !ok || record.Kind != KindGo || record.BinaryPath == "" {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.NotFound, Message: fmt.Sprintf("go skill %q not found", args.SkillName)}, nil
	}
	preview := fmt.Sprintf("Go Skill：%s\n程序：%s\n工作目录：%s", record.Name, record.BinaryPath, record.Root)
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: record.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.TextConfirmationDetail(preview)}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		return r.call(ctx, args, record, runMediaCommand)
	})}, nil
}

func (r GoRunner) call(ctx context.Context, args goRunnerArgs, record record, run func(context.Context, string, *exec.Cmd, toolshare.MediaCall) (operationresult.Result[*toolshare.Result], error)) (operationresult.Result[*toolshare.Result], string, error) {

	dotEnv, err := config.LoadEnvFile(filepath.Join(record.Root, ".env"))
	if err != nil {
		callErr := fmt.Errorf("load Go skill environment: %w", err)
		return operationresult.Result[*toolshare.Result]{}, callErr.Error(), callErr
	}
	environment := r.manager.processEnv.Overlay(dotEnv)
	timeout := runnerTimeout(args.TimeoutMS)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	mediaCall := r.manager.media.NewCall()
	defer mediaCall.Close()
	payload, err := prepareGoMedia(runCtx, args.Payload, mediaCall, record.Root)
	if err != nil {
		callErr := fmt.Errorf("prepare Go skill media: %w", err)
		return operationresult.Result[*toolshare.Result]{}, callErr.Error(), callErr
	}
	if payload.Status != operationresult.Success {
		return operationresult.Result[*toolshare.Result]{Status: payload.Status, Message: payload.Message}, payload.Message, nil
	}
	cmd := environment.CommandContext(runCtx, record.BinaryPath)
	cmd.Dir = record.Root
	cmd.Stdin = bytes.NewReader(payload.Data)
	result, callErr := run(runCtx, "go skill", cmd, mediaCall)
	if callErr != nil {
		return operationresult.Result[*toolshare.Result]{}, "", callErr
	}
	return result, "", nil
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
	if payload == nil {
		return goRunnerArgs{}, fmt.Errorf("payload must be object")
	}
	args.Payload = json.RawMessage(value)
	return args, nil
}
