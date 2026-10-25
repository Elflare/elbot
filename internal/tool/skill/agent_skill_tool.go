// 将文档型 Skill 适配为可发现的工具入口。
package skill

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"elbot/internal/llm"
)

type AgentSkillTool struct {
	Manager *Manager
}

type agentSkillArgs struct {
	Action string `json:"action"`
	Name   string `json:"name"`
	Toml   string `json:"toml"`
}

func NewAgentSkillTool(manager *Manager) AgentSkillTool {
	return AgentSkillTool{Manager: manager}
}

func (AgentSkillTool) Name() string { return AgentSkillManagerName }

func (AgentSkillTool) Info() toolshare.Info {
	return toolshare.NewBuilder(AgentSkillManagerName).
		Description(agentSkillToolDescription()).
		Source(toolshare.SourceBuiltin).
		Risk(toolshare.RiskHigh).
		Hidden().
		BuildInfo()
}

func (AgentSkillTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{
		Name:        AgentSkillManagerName,
		Description: agentSkillToolDescription(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"read", "write"}, "description": "read 或 write。"},
				"name":   map[string]any{"type": "string", "description": "AgentSkill 名称。"},
				"toml":   map[string]any{"type": "string", "description": "write 时写入的完整 " + AgentSkillConfigFile + " 内容。"},
			},
			"required": []string{"action", "name"},
		},
	}
}

func agentSkillToolDescription() string {
	return "读或写 AgentSkill 的 ELBOT_SKILL.toml。"
}

func (t AgentSkillTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	preview, err := t.preview(ctx, req)
	if err != nil {
		return nil, err
	}
	detail := fmt.Sprintf("写入 AgentSkill %s 的 %s\n文件：%s", preview.Name, AgentSkillConfigFile, preview.Path)
	if preview.Action == "read" {
		detail = fmt.Sprintf("读取 AgentSkill %s 的 %s", preview.Name, AgentSkillConfigFile)
	}
	return toolshare.NewPreparedCall(req, detail, func(ctx context.Context) (*toolshare.Result, string, error) {
		return t.call(ctx, preview)
	}), nil
}

func (t AgentSkillTool) Call(ctx context.Context, req toolshare.CallRequest) (_ *toolshare.Result, logContent string, _ error) {
	preview, err := t.preview(ctx, req)
	if err != nil {
		return nil, toolshare.TruncateLogContent(err.Error(), 1024), err
	}
	return t.call(ctx, preview)
}

func (t AgentSkillTool) call(ctx context.Context, preview agentSkillPreview) (_ *toolshare.Result, logContent string, _ error) {
	defer func() { logContent = toolshare.TruncateLogContent(logContent, 1024) }()
	if err := ctx.Err(); err != nil {
		return nil, err.Error(), err
	}
	record, ok := t.Manager.Catalog.Get(preview.Name)
	if !ok || record.Kind != KindAgent || AgentSkillConfigPath(record.Root) != preview.Path {
		err := fmt.Errorf("AgentSkill target changed after preflight")
		return nil, err.Error(), err
	}
	if preview.Action == "read" {
		{
			result := &toolshare.Result{Content: readAgentSkillConfig(record, preview.Path)}
			return result, result.Content, nil
		}
	}
	if err := t.Manager.WriteAgentSkillConfig(ctx, preview.Name, preview.Toml); err != nil {
		{
			callErr := fmt.Errorf("write %s for AgentSkill %s: %w", AgentSkillConfigFile, preview.Name, err)
			return nil, callErr.Error(), callErr
		}
	}
	{
		result := &toolshare.Result{Content: fmt.Sprintf("wrote %s for AgentSkill %s and reloaded skills", AgentSkillConfigFile, preview.Name)}
		return result, result.Content, nil
	}
}

type agentSkillPreview struct {
	Action string
	Name   string
	Path   string
	Toml   string
}

func (t AgentSkillTool) preview(ctx context.Context, req toolshare.CallRequest) (agentSkillPreview, error) {
	if err := ctx.Err(); err != nil {
		return agentSkillPreview{}, err
	}
	var args agentSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return agentSkillPreview{}, fmt.Errorf("parse agent_skill arguments: %w", err)
		}
	}
	name := strings.TrimSpace(args.Name)
	if err := validateSkillName(name); err != nil {
		return agentSkillPreview{}, err
	}
	record, ok := t.Manager.Catalog.Get(name)
	if !ok || record.Kind != KindAgent {
		return agentSkillPreview{}, fmt.Errorf("AgentSkill %q not found", name)
	}
	path := AgentSkillConfigPath(record.Root)
	action := strings.TrimSpace(args.Action)
	switch action {
	case "read":
		return agentSkillPreview{Action: action, Name: name, Path: path}, nil
	case "write":
		if strings.TrimSpace(args.Toml) == "" {
			return agentSkillPreview{}, fmt.Errorf("toml is required for write")
		}
		if _, err := ParseAgentSkillManifest([]byte(args.Toml)); err != nil {
			return agentSkillPreview{}, err
		}
		return agentSkillPreview{Action: action, Name: name, Path: path, Toml: args.Toml}, nil
	default:
		return agentSkillPreview{}, fmt.Errorf("invalid action %q; use read or write", args.Action)
	}
}

func readAgentSkillConfig(record Record, path string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "AgentSkill: %s\n", record.Name)
	fmt.Fprintf(&b, "Config file: %s\n", AgentSkillConfigFile)
	if record.ManifestFound && record.ManifestError == "" && record.Manifest.Callable {
		b.WriteString("Status: callable\n")
	} else if record.ManifestFound && record.ManifestError == "" {
		b.WriteString("Status: document-only\nPolicy: configured\n")
	} else if record.ManifestFound {
		fmt.Fprintf(&b, "Status: invalid\nError: %s\n", record.ManifestError)
	} else {
		b.WriteString("Status: document-only\n")
	}
	if data, err := os.ReadFile(path); err == nil {
		b.WriteString("\nCurrent TOML:\n")
		b.Write(data)
		if !strings.HasSuffix(string(data), "\n") {
			b.WriteByte('\n')
		}
	} else if os.IsNotExist(err) {
		b.WriteString("\nNo current TOML.\n")
	}
	b.WriteString("\nFormat:\n")
	b.WriteString("# 文档型 Skill 只限制可见性时，可以只写：\nrisk = \"high\"\nsuperadmin_only = true\n\n# 注册成普通工具时，补齐命令和 schema：\nrisk = \"medium\"\nsuperadmin_only = false\ntags = [\"doc\"]\ncommand = [\"python\", \"foo.py\"]\ntimeout_seconds = 30\nexpose_root = false\n\nparameters = '''\n{\n  \"type\": \"object\",\n  \"required\": [\"input\"],\n  \"properties\": {\n    \"input\": {\"type\": \"string\", \"description\": \"输入文本\"},\n    \"mode\": {\"type\": \"string\", \"description\": \"处理模式\"}\n  }\n}\n'''\n\n[args]\ninput = \"--input\"\nmode = \"--mode\"\n")
	return strings.TrimRight(b.String(), "\n")
}
