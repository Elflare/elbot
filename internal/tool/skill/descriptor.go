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
)

const (
	AgentSkillManagerName = "agent_skill"
	GoRunnerName          = "go_skill_run"
)

type descriptor struct {
	record record // 扫描时确定的技能声明；详情正文按需读取。
}

func newDescriptor(record record) descriptor {
	return descriptor{record: record}
}

func (d descriptor) Name() string { return d.record.Name }

func (d descriptor) Info() toolshare.Info {
	return toolshare.Info{
		Name:           d.record.Name,
		Description:    d.record.Description,
		Source:         SourceForKind(d.record.Kind),
		Risk:           d.record.Risk,
		SuperadminOnly: d.record.SuperadminOnly,
		Tags:           d.record.Tags,
	}
}

func (d descriptor) Schema() llm.ToolSchema {
	// Skill 本体是说明文档，不是可直接调用的 function schema。
	return llm.ToolSchema{}
}

func (d descriptor) PrepareCall(context.Context, toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return nil, fmt.Errorf("skill %q is a document skill; query it with discover_tool", d.record.Name)
}

func (d descriptor) Detail() string {
	block, err := d.LoadDetail(context.Background())
	if err != nil {
		return ""
	}
	return toolshare.RenderDetailBlocks([]toolshare.DetailBlock{block})
}

func (d descriptor) DetailBlock() toolshare.DetailBlock {
	block, _ := d.LoadDetail(context.Background())
	return block
}

func (d descriptor) LoadDetail(ctx context.Context) (toolshare.DetailBlock, error) {
	block, err := loadRecordDetail(d.record)
	if err != nil {
		return toolshare.DetailBlock{}, err
	}
	if d.record.Kind == KindAgent {
		block.Content = strings.TrimSpace(block.Content + "\n\n" + agentSkillNotice(ctx, d.record))
	}
	return block, nil
}

func (d descriptor) ActivateTools() []string {
	switch d.record.Kind {
	case KindAgent:
		return []string{AgentSkillManagerName}
	case KindGo:
		if d.record.BinaryPath != "" {
			return []string{GoRunnerName}
		}
		return nil
	default:
		return nil
	}
}

func agentSkillNotice(ctx context.Context, record record) string {
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

func loadRecordDetail(record record) (toolshare.DetailBlock, error) {
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
