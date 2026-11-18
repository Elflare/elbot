// 将 Skill 命令适配为工具调用。
package skill

import toolshare "elbot/internal/tool/share"

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"elbot/internal/config"
	"elbot/internal/operationresult"
	"elbot/internal/processenv"
	"elbot/internal/workspace"
)

type commandTool struct {
	media    interface{ NewCall() toolshare.MediaCall } // 调用媒体的导入导出与清理入口。
	record   record                                     // 注册时选定的技能记录。
	manifest AgentSkillManifest                         // 准备和执行共用的命令声明。
	// ProcessEnv 保存启动时已合并配置目录 .env 的基础子进程环境。
	processEnv processenv.Environment
}

func (t commandTool) Definition() toolshare.ToolDefinition {
	parameters := projectMediaSchema(t.manifest.Parameters).(map[string]any)
	return toolshare.ToolDefinition{Definition: toolshare.Info{Name: t.record.Name, Description: t.record.Description, Risk: t.manifest.Risk, Weavable: t.manifest.Weavable, SuperadminOnly: t.manifest.SuperadminOnly, Tags: t.manifest.Tags, DependsOn: t.manifest.DependsOn}, Parameters: parameters, Prepare: t.PrepareCall, LoadDetail: t.LoadDetail, ActivateTools: t.ActivateTools()}
}

func (t commandTool) LoadDetail(ctx context.Context) (toolshare.DetailBlock, error) {
	block, err := loadRecordDetail(t.record)
	if err != nil {
		return toolshare.DetailBlock{}, err
	}
	if t.manifest.ExposeRoot {
		block.Content = strings.TrimSpace(block.Content) + "\n\nAgentSkill root: " + t.record.Root
	}
	block.Content = strings.TrimSpace(block.Content)
	return block, nil
}

func (t commandTool) ActivateTools() []string {
	return []string{AgentSkillManagerName, t.record.Name}
}

func (t commandTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (operationresult.Result[*toolshare.PreparedCall], error) {
	raw := bytes.Clone(req.Arguments)
	args, err := commandArguments(t.manifest, raw)
	if err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	workDir := t.record.Root
	if t.manifest.UseWorkspace {
		workDir, err = workspace.CurrentWorkspaceDir(ctx)
		if err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Unavailable, Message: fmt.Sprintf("resolve AgentSkill workspace: %v", err)}, nil
		}
	}
	details := []toolshare.ConfirmationDetailBlock{toolshare.FieldsConfirmationDetail(
		toolshare.ConfirmationField{Key: "skill_name", Label: "技能", Value: t.record.Name},
		toolshare.ConfirmationField{Key: "command", Label: "命令", Value: t.manifest.Command},
		toolshare.ConfirmationField{Key: "args", Label: "命令行参数", Value: args},
		toolshare.ConfirmationField{Key: "work_dir", Label: "工作目录", Value: workDir},
		toolshare.ConfirmationField{Key: "timeout_ms", Label: "超时（ms）", Value: int64(t.manifest.TimeoutSeconds) * 1000},
	)}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.manifest.Risk}, details, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		return t.call(ctx, raw, args, workDir, runMediaCommand)
	})}, nil
}

func (t commandTool) call(ctx context.Context, raw json.RawMessage, args []string, workDir string, run func(context.Context, string, *exec.Cmd, toolshare.MediaCall) (operationresult.Result[*toolshare.Result], error)) (operationresult.Result[*toolshare.Result], string, error) {

	dotEnv, err := config.LoadEnvFile(filepath.Join(t.record.Root, ".env"))
	if err != nil {
		callErr := fmt.Errorf("load AgentSkill environment: %w", err)
		return operationresult.Result[*toolshare.Result]{}, callErr.Error(), callErr
	}
	environment := t.processEnv.Overlay(dotEnv)

	timeout := time.Duration(t.manifest.TimeoutSeconds) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	mediaCall := t.media.NewCall()
	defer mediaCall.Close()
	if schemaContainsMedia(t.manifest.Parameters) {
		prepared, err := prepareCommandMedia(runCtx, raw, t.manifest, mediaCall, workDir)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if prepared.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: prepared.Status, Message: prepared.Message}, prepared.Message, nil
		}
		args, err = commandArguments(t.manifest, prepared.Data)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
	}
	command := append([]string(nil), t.manifest.Command...)
	command = append(command, args...)
	cmd := environment.CommandContext(runCtx, command[0], command[1:]...)
	cmd.Dir = workDir
	result, callErr := run(runCtx, "AgentSkill command", cmd, mediaCall)
	if callErr != nil {
		return operationresult.Result[*toolshare.Result]{}, "", callErr
	}
	return result, "", nil
}

func commandArguments(manifest AgentSkillManifest, raw json.RawMessage) ([]string, error) {
	values := map[string]json.RawMessage{}
	if len(raw) > 0 && strings.TrimSpace(string(raw)) != "" {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("parse skill tool arguments: %w", err)
		}
	}
	for _, name := range schemaRequired(manifest.Parameters) {
		if _, ok := values[name]; !ok {
			return nil, fmt.Errorf("required argument %s is missing", name)
		}
	}
	out := []string{}
	for _, name := range manifest.Positional {
		value, ok := values[name]
		if !ok || string(value) == "null" {
			continue
		}
		out = append(out, argumentText(value))
	}
	names := make([]string, 0, len(manifest.Args))
	for name := range manifest.Args {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value, ok := values[name]
		if !ok || string(value) == "null" {
			continue
		}
		out = append(out, manifest.Args[name], argumentText(value))
	}
	return out, nil
}

// argumentText 接收 commandArguments 已解析且排除 null 的参数值。
func argumentText(raw json.RawMessage) string {
	if raw[0] == '"' {
		var text string
		_ = json.Unmarshal(raw, &text)
		return text
	}
	var compact bytes.Buffer
	_ = json.Compact(&compact, raw)
	return compact.String()
}
