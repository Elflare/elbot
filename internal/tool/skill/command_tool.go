// 将 Skill 命令适配为工具调用。
package skill

import toolshare "elbot/internal/tool/share"

import (
	"bytes"
	"context"
	toolmedia "elbot/internal/tool/media"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/processenv"
	"elbot/internal/tool"
)

type CommandTool struct {
	Media    *toolmedia.MediaRuntime
	Record   Record
	Manifest AgentSkillManifest
	// ProcessEnv 保存启动时已合并配置目录 .env 的基础子进程环境。
	ProcessEnv processenv.Environment
}

func NewCommandTool(record Record) CommandTool {
	return CommandTool{Record: record, Manifest: record.Manifest}
}

func (t CommandTool) Name() string { return t.Record.Name }

func (t CommandTool) Info() toolshare.Info {
	return toolshare.Info{Name: t.Record.Name, Description: t.Record.Description, Source: SourceForKind(t.Record.Kind), Risk: t.Manifest.Risk, SuperadminOnly: t.Manifest.SuperadminOnly, Tags: t.Manifest.Tags}
}

func (t CommandTool) Schema() llm.ToolSchema {
	return t.Manifest.Schema(t.Record.Name, t.Record.Description)
}

func (t CommandTool) Detail() string {
	block, err := t.LoadDetail(context.Background())
	if err != nil {
		return ""
	}
	return tool.RenderDetailBlocks([]toolshare.DetailBlock{block})
}

func (t CommandTool) DetailBlock() toolshare.DetailBlock {
	block, _ := t.LoadDetail(context.Background())
	return block
}

func (t CommandTool) LoadDetail(ctx context.Context) (toolshare.DetailBlock, error) {
	block, err := loadRecordDetail(t.Record)
	if err != nil {
		return toolshare.DetailBlock{}, err
	}
	if t.Manifest.ExposeRoot {
		block.Content = strings.TrimSpace(block.Content) + "\n\nAgentSkill root: " + t.Record.Root
	}
	block.Content = strings.TrimSpace(block.Content)
	return block, nil
}

func (t CommandTool) ActivateTools() []string {
	return []string{AgentSkillManagerName, t.Record.Name}
}

func (t CommandTool) Call(ctx context.Context, req toolshare.CallRequest) (result *toolshare.Result, logContent string, callErr error) {
	return t.call(ctx, req, runMediaCommand)
}

func (t CommandTool) call(ctx context.Context, req toolshare.CallRequest, run func(context.Context, string, *exec.Cmd, *toolmedia.MediaCall) (*toolshare.Result, error)) (result *toolshare.Result, logContent string, callErr error) {
	defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()

	dotEnv, err := config.LoadEnvFile(filepath.Join(t.Record.Root, ".env"))
	if err != nil {
		callErr := fmt.Errorf("load AgentSkill environment: %w", err)
		return nil, callErr.Error(), callErr
	}
	environment := t.ProcessEnv.Overlay(dotEnv)

	timeout := defaultRunnerTimeout
	if t.Manifest.TimeoutSeconds > 0 {
		timeout = time.Duration(t.Manifest.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	mediaCall := t.Media.NewCall()
	defer func() {
		err := mediaCall.Close()
		callErr = errors.Join(callErr, err)
		if err != nil {
			logContent = fmt.Sprintf("media_cleanup_error=%v\n%s", err, logContent)
		}
	}()
	raw, err := prepareCommandMedia(runCtx, req.Arguments, t.Manifest, mediaCall, t.Record.Root)
	if err != nil {
		return nil, err.Error(), err
	}
	args, err := commandArguments(t.Manifest, raw)
	if err != nil {
		return nil, err.Error(), err
	}
	command := append([]string(nil), t.Manifest.Command...)
	command = append(command, args...)
	cmd := environment.CommandContext(runCtx, command[0], command[1:]...)
	cmd.Dir = t.Record.Root
	{
		result, callErr := run(runCtx, "AgentSkill command", cmd, mediaCall)
		logContent = ""
		if result != nil {
			logContent = result.Content
		}
		if callErr != nil {
			logContent = callErr.Error() + "\n" + logContent
		}
		return result, logContent, callErr
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
