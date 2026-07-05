package skill

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/elyph"
	"elbot/internal/llm"
	"elbot/internal/tool"
)

const (
	AgentSkillManagerName = "agent_skill"
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
	return nil, fmt.Errorf("skill %q is a document skill; query it with discover_tool", d.Record.Name)
}

func (d Descriptor) Detail() string {
	return tool.RenderDetailBlocks([]tool.DetailBlock{d.DetailBlock()})
}

func (d Descriptor) DetailBlock() tool.DetailBlock {
	content := d.Record.Detail
	if d.Record.Kind == KindAgent {
		content = strings.TrimSpace(content + "\n\n" + agentSkillNotice(d.Record))
	}
	block := tool.DetailBlock{Content: content, Format: d.Record.Format}
	if d.Record.Format == elyph.Format {
		block.RuleCard = elyph.RuleCard()
	}
	return block
}

func (d Descriptor) ActivateTools() []string {
	switch d.Record.Kind {
	case KindAgent:
		return []string{AgentSkillManagerName}
	case KindGo:
		if d.Record.BinaryPath != "" {
			return []string{GoRunnerName}
		}
		return nil
	default:
		return nil
	}
}

func agentSkillNotice(record Record) string {
	lines := []string{"ElBot AgentSkill 使用提示：", "", "- 如该文档有脚本，请发现 agent_skill_creator，参考其说明是否把他注册成普通工具。"}
	if record.ManifestFound && record.ManifestError != "" {
		lines = append(lines, "- 当前 "+AgentSkillConfigFile+" 无效："+record.ManifestError)
	}
	return strings.Join(lines, "\n")
}
