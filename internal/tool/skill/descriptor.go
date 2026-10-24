// 描述 Skill 元数据、工具声明及详情。
package skill

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"fmt"
	"os"
	"strings"

	"elbot/internal/contextinfo"
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

func (d Descriptor) Info() toolshare.Info {
	return toolshare.Info{
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

func (d Descriptor) Call(context.Context, toolshare.CallRequest) (*toolshare.Result, string, error) {
	{
		callErr := fmt.Errorf("skill %q is a document skill; query it with discover_tool", d.Record.Name)
		return nil, toolshare.TruncateLogContent(callErr.Error(), 1024), callErr
	}
}

func (d Descriptor) Detail() string {
	block, err := d.LoadDetail(context.Background())
	if err != nil {
		return ""
	}
	return tool.RenderDetailBlocks([]toolshare.DetailBlock{block})
}

func (d Descriptor) DetailBlock() toolshare.DetailBlock {
	block, _ := d.LoadDetail(context.Background())
	return block
}

func (d Descriptor) LoadDetail(ctx context.Context) (toolshare.DetailBlock, error) {
	block, err := loadRecordDetail(d.Record)
	if err != nil {
		return toolshare.DetailBlock{}, err
	}
	if d.Record.Kind == KindAgent {
		block.Content = strings.TrimSpace(block.Content + "\n\n" + agentSkillNotice(ctx, d.Record))
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

func agentSkillNotice(ctx context.Context, record Record) string {
	var lines []string
	if actor, ok := contextinfo.ActorFromContext(ctx); ok && actor.Role == contextinfo.RoleSuperadmin {
		lines = append(lines, "- 如该文档有脚本，请发现 agent_skill_creator，参考其说明是否把他注册成普通工具。")
	}
	if record.ManifestFound && record.ManifestError != "" {
		lines = append(lines, "- 当前 "+AgentSkillConfigFile+" 无效："+record.ManifestError)
	}
	if len(lines) == 0 {
		return ""
	}
	return "ElBot AgentSkill 使用提示：\n\n" + strings.Join(lines, "\n")
}

func loadRecordDetail(record Record) (toolshare.DetailBlock, error) {
	content := record.Detail
	format := record.Format
	if record.DetailPath != "" {
		data, err := os.ReadFile(record.DetailPath)
		if err != nil {
			return toolshare.DetailBlock{}, fmt.Errorf("read SKILL.md for %q: %w", record.Name, err)
		}
		def, err := ParseSkillMarkdown(data, record.Name)
		if err != nil {
			return toolshare.DetailBlock{}, fmt.Errorf("parse SKILL.md for %q: %w", record.Name, err)
		}
		content = def.Detail
		format = def.Format
	}
	return toolshare.DetailBlock{Content: content, Format: format}, nil
}
