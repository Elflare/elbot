package skill

import (
	"context"
	"fmt"

	"elbot/internal/elyph"
	"elbot/internal/llm"
	"elbot/internal/tool"
)

const (
	PythonRunnerName = "python_skill_run"
	GoRunnerName     = "go_skill_run"
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
	// Skill 本体是 markdown 说明，不是可直接调用的 function schema。
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
	if d.Record.Kind == KindPython {
		detail = detail + pythonConstraint()
	}
	return detail
}

func (d Descriptor) ActivateTools() []string {
	switch d.Record.Kind {
	case KindPython:
		return []string{PythonRunnerName}
	case KindGo:
		if d.Record.BinaryPath != "" {
			return []string{GoRunnerName}
		}
		return nil
	default:
		return nil
	}
}

func pythonConstraint() string {
	return "\n\n---\n\nElBot Python skill 运行约束：\n" +
		"- 如果需要执行此 skill 附带的 Python 脚本，请使用 python_skill_run。\n" +
		"- python_skill_run 会在 skill 目录下通过 uv run python 执行脚本。\n" +
		"- 不要照抄原文中的 python、pip、venv、conda 环境命令。\n" +
		"- 若原说明确实要求 bash/系统命令，可使用 shell 工具并遵守风险确认流程。"
}
