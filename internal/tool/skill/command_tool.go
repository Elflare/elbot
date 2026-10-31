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
	"elbot/internal/llm"
	"elbot/internal/processenv"
)

type commandTool struct {
	media    interface{ NewCall() toolshare.MediaCall } // 调用媒体的导入导出与清理入口。
	record   record                                     // 注册时选定的技能记录。
	manifest AgentSkillManifest                         // 准备和执行共用的命令声明。
	// ProcessEnv 保存启动时已合并配置目录 .env 的基础子进程环境。
	processEnv processenv.Environment
}

func newCommandTool(record record, media interface{ NewCall() toolshare.MediaCall }, environment processenv.Environment) commandTool {
	return commandTool{record: record, manifest: record.Manifest, media: media, processEnv: environment}
}

func (t commandTool) Name() string { return t.record.Name }

func (t commandTool) Info() toolshare.Info {
	return toolshare.Info{Name: t.record.Name, Description: t.record.Description, Source: SourceForKind(t.record.Kind), Risk: t.manifest.Risk, SuperadminOnly: t.manifest.SuperadminOnly, Tags: t.manifest.Tags}
}

func (t commandTool) Schema() llm.ToolSchema {
	return t.manifest.Schema(t.record.Name, t.record.Description)
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

func (t commandTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	raw := bytes.Clone(req.Arguments)
	args, err := commandArguments(t.manifest, raw)
	if err != nil {
		return nil, err
	}
	preview := fmt.Sprintf("命令：%v\n参数：%v\n工作目录：%s", t.manifest.Command, args, t.record.Root)
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, preview, func(ctx context.Context) (*toolshare.Result, string, error) {
		return t.call(ctx, raw, args, runMediaCommand)
	}), nil
}

func (t commandTool) call(ctx context.Context, raw json.RawMessage, args []string, run func(context.Context, string, *exec.Cmd, toolshare.MediaCall) (*toolshare.Result, error)) (*toolshare.Result, string, error) {

	dotEnv, err := config.LoadEnvFile(filepath.Join(t.record.Root, ".env"))
	if err != nil {
		callErr := fmt.Errorf("load AgentSkill environment: %w", err)
		return nil, callErr.Error(), callErr
	}
	environment := t.processEnv.Overlay(dotEnv)

	timeout := defaultRunnerTimeout
	if t.manifest.TimeoutSeconds > 0 {
		timeout = time.Duration(t.manifest.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	mediaCall := t.media.NewCall()
	defer mediaCall.Close()
	if schemaContainsMedia(t.manifest.Parameters) {
		prepared, err := prepareCommandMedia(runCtx, raw, t.manifest, mediaCall, t.record.Root)
		if err != nil {
			return nil, err.Error(), err
		}
		args, err = commandArguments(t.manifest, prepared)
		if err != nil {
			return nil, err.Error(), err
		}
	}
	command := append([]string(nil), t.manifest.Command...)
	command = append(command, args...)
	cmd := environment.CommandContext(runCtx, command[0], command[1:]...)
	cmd.Dir = t.record.Root
	{
		result, callErr := run(runCtx, "AgentSkill command", cmd, mediaCall)
		return result, "", callErr
	}
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
	names := make([]string, 0, len(manifest.Args))
	for name := range manifest.Args {
		names = append(names, name)
	}
	sort.Strings(names)
	out := []string{}
	for _, name := range names {
		value, ok := values[name]
		if !ok || string(value) == "null" {
			continue
		}
		text, err := argumentText(value)
		if err != nil {
			return nil, fmt.Errorf("argument %s: %w", name, err)
		}
		out = append(out, manifest.Args[name], text)
	}
	return out, nil
}

func argumentText(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		return number.String(), nil
	}
	var boolean bool
	if err := json.Unmarshal(raw, &boolean); err == nil {
		if boolean {
			return "true", nil
		}
		return "false", nil
	}
	var complex any
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&complex); err == nil {
		switch complex.(type) {
		case []any, map[string]any:
			var compact bytes.Buffer
			if err := json.Compact(&compact, raw); err == nil {
				return compact.String(), nil
			}
		}
	}
	return "", fmt.Errorf("must be string, number, boolean, array, or object")
}
