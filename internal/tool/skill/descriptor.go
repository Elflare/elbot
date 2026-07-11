package skill

import (
	"context"
	"fmt"
	"os"
	"strings"

	"elbot/internal/elyph"
	"elbot/internal/llm"
	"elbot/internal/tool"
	"elbot/internal/tool/runtimeinfo"
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
		Name:           d.Record.Name,
		Description:    d.Record.Description,
		Source:         SourceForKind(d.Record.Kind),
		Risk:           d.Record.Risk,
		SuperadminOnly: d.Record.SuperadminOnly,
		Tags:           d.Record.Tags,
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
	block, err := d.LoadDetail()
	if err != nil {
		return ""
	}
	return tool.RenderDetailBlocks([]tool.DetailBlock{block})
}

func (d Descriptor) DetailBlock() tool.DetailBlock {
	block, _ := d.LoadDetail()
	return block
}

func (d Descriptor) LoadDetail() (tool.DetailBlock, error) {
	block, err := loadRecordDetail(d.Record)
	if err != nil {
		return tool.DetailBlock{}, err
	}
	if d.Record.Kind == KindAgent {
		block.Content = strings.TrimSpace(block.Content + "\n\n" + agentSkillNotice(d.Record))
	}
	return block, nil
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

func loadRecordDetail(record Record) (tool.DetailBlock, error) {
	content := record.Detail
	format := record.Format
	if record.DetailPath != "" {
		data, err := os.ReadFile(record.DetailPath)
		if err != nil {
			return tool.DetailBlock{}, fmt.Errorf("read SKILL.md for %q: %w", record.Name, err)
		}
		def, err := ParseSkillMarkdown(data, record.Name)
		if err != nil {
			return tool.DetailBlock{}, fmt.Errorf("parse SKILL.md for %q: %w", record.Name, err)
		}
		content = def.Detail
		format = def.Format
	}
	block := tool.DetailBlock{Content: content, Format: format}
	if format == elyph.Format {
		block.RuleCard = runtimeinfo.ElyphRuleCard()
	}
	return block, nil
}
