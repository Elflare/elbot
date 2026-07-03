package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"elbot/internal/llm"
	"elbot/internal/tool"
	"elbot/internal/utils/fileops"
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

func (AgentSkillTool) Info() tool.Info {
	return tool.NewBuilder(AgentSkillManagerName).
		Description(agentSkillToolDescription()).
		Source(tool.SourceBuiltin).
		Risk(tool.RiskHigh).
		Hidden().
		BuildInfo()
}

func (AgentSkillTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Type: "function", Function: llm.ToolFunctionSchema{
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
	}}
}

func agentSkillToolDescription() string {
	return `读或写 AgentSkill 的 ELBOT_SKILL.toml。
if a skill 带脚本:
    if 你没有该脚本的Schema只能用shell来运行 and 你觉得使用shell很麻烦:
        使用该工具创建 ELBOT_SKILL.toml，之后会自动注入你的Schema，之后可以不使用shell运行
elif:
    检查你已知Schema工具，是否有该技能的脚本，就可以直接调用，而不用shell
ELBOT_SKILL.toml写法：
只允许这些字段：
risk, tags, command, timeout_seconds, expose_root, parameters, [args]

示例：
risk = "medium"
tags = ["doc"]
command = ["python", "foo.py"]
timeout_seconds = 30
expose_root = false

parameters = '''
{
  "type": "object",
  "required": ["input"],
  "properties": {
    "input": {"type": "string", "description": "输入文本"},
    "mode": {"type": "string", "description": "处理模式"}
  }
}
'''

[args]
input = "--input"
mode = "--mode"

含义：
工具调用 {"input":"abc","mode":"fast"} 会执行：
python foo.py --input abc --mode fast

command 必须是字符串数组。
parameters 必须是 JSON object schema。
parameters.properties 定义工具有哪些入参；[args] 的 key 必须对应 parameters.properties。
risk 必填。
tags 可选，相当于为该工具分类。`
}

func (t AgentSkillTool) PreflightConfirmation(ctx context.Context, req tool.CallRequest) error {
	_, err := t.preview(ctx, req)
	return err
}

func (t AgentSkillTool) RiskDetail(ctx context.Context, req tool.CallRequest) (string, error) {
	preview, err := t.preview(ctx, req)
	if err != nil {
		return "", err
	}
	if preview.Action == "read" {
		return fmt.Sprintf("读取 AgentSkill %s 的 %s", preview.Name, AgentSkillConfigFile), nil
	}
	return fmt.Sprintf("写入 AgentSkill %s 的 %s\n文件：%s", preview.Name, AgentSkillConfigFile, preview.Path), nil
}

func (t AgentSkillTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	preview, err := t.preview(ctx, req)
	if err != nil {
		return nil, err
	}
	if preview.Action == "read" {
		return &tool.Result{Content: preview.ReadContent}, nil
	}
	if err := fileops.AtomicWriteFile(preview.Path, []byte(preview.Toml), existingFileMode(preview.Path)); err != nil {
		return nil, err
	}
	if err := t.Manager.Reload(ctx); err != nil {
		return nil, fmt.Errorf("%s written but reload failed: %w", AgentSkillConfigFile, err)
	}
	return &tool.Result{Content: fmt.Sprintf("wrote %s for AgentSkill %s and reloaded skills", AgentSkillConfigFile, preview.Name)}, nil
}

type agentSkillPreview struct {
	Action      string
	Name        string
	Path        string
	Toml        string
	ReadContent string
}

func (t AgentSkillTool) preview(ctx context.Context, req tool.CallRequest) (agentSkillPreview, error) {
	if err := ctx.Err(); err != nil {
		return agentSkillPreview{}, err
	}
	var args agentSkillArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return agentSkillPreview{}, fmt.Errorf("parse agent_skill arguments: %w", err)
		}
	}
	if t.Manager == nil || t.Manager.Catalog == nil {
		return agentSkillPreview{}, fmt.Errorf("skill manager is not configured")
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
		return agentSkillPreview{Action: action, Name: name, Path: path, ReadContent: readAgentSkillConfig(record, path)}, nil
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
	if record.ManifestFound && record.ManifestError == "" {
		b.WriteString("Status: callable\n")
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
	b.WriteString("risk = \"medium\"\ntags = [\"doc\"]\ncommand = [\"python\", \"foo.py\"]\ntimeout_seconds = 30\nexpose_root = false\n\nparameters = '''\n{\n  \"type\": \"object\",\n  \"required\": [\"input\"],\n  \"properties\": {\n    \"input\": {\"type\": \"string\", \"description\": \"输入文本\"},\n    \"mode\": {\"type\": \"string\", \"description\": \"处理模式\"}\n  }\n}\n'''\n\n[args]\ninput = \"--input\"\nmode = \"--mode\"\n")
	return strings.TrimRight(b.String(), "\n")
}
