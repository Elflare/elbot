package skill

import (
	"context"
	"fmt"

	"elbot/internal/elyph"
	"elbot/internal/llm"
	"elbot/internal/tool"
)

const (
	AgentScriptRunnerName = "python_skill_run"
	GoRunnerName          = "go_skill_run"
)

type DetailProvider interface {
	Detail() string
	ActivateTools() []string
}

type Descriptor struct {
	Record Record
}

func NewDescriptor(record Record) Descriptor {
	return Descriptor{Record: record}
}

func (d Descriptor) Name() string { return d.Record.Name }

func (d Descriptor) Info() tool.Info {
	return tool.Info{
		Name:        d.Record.Name,
		Description: d.Record.Description,
		Source:      SourceForKind(d.Record.Kind),
		Risk:        d.Record.Risk,
	}
}

func (d Descriptor) Schema() llm.ToolSchema {
	// Skill 本体是说明文档，不是可直接调用的 function schema。
	return llm.ToolSchema{}
}

func (d Descriptor) Call(context.Context, tool.CallRequest) (*tool.Result, error) {
	return nil, fmt.Errorf("skill %q is a document skill; query it with discover_tool and use the activated wrapper tool", d.Record.Name)
}

func (d Descriptor) Detail() string {
	detail := d.Record.Detail
	if d.Record.Format == elyph.Format {
		detail = elyph.RuleCard() + "\n\n" + detail
	}
	if d.Record.Kind == KindAgent {
		detail = detail + agentSkillPythonConstraint()
	}

	return detail
}

func (d Descriptor) ActivateTools() []string {
	switch d.Record.Kind {
	case KindAgent:
		// TODO: 根据 AgentSkill 声明的脚本类型选择 wrapper；当前仅支持 Python 脚本。
		return []string{AgentScriptRunnerName}
	case KindGo:
		if d.Record.BinaryPath != "" {
			return []string{GoRunnerName}
		}
		return nil
	default:
		return nil
	}
}

func agentSkillPythonConstraint() string {
	return "\n\n---\n\nElBot AgentSkill Python 脚本运行约束：\n" +
		"- 如果需要执行此 AgentSkill 附带的 Python 脚本，请使用 python_skill_run。\n" +
		"- python_skill_run 会在 AgentSkill 目录下通过 uv run python 执行脚本。\n" +
		"- 不要照抄原文中的 python、pip、venv、conda 环境命令。\n" +
		"- 不要用 shell 猜测或访问 AgentSkill 安装目录；shell 仅用于当前任务 sandbox 内普通命令。"
}
