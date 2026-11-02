// 描述 Skill 元数据、工具声明及详情。
package skill

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"fmt"
	"os"
	"strings"

	"elbot/internal/contextinfo"
)

const (
	AgentSkillManagerName = "agent_skill"
	GoRunnerName          = "go_skill_run"
)

type descriptor struct {
	record record // 扫描时确定的技能声明；详情正文按需读取。
}

func (t descriptor) Definition() toolshare.ToolDefinition {
	return toolshare.ToolDefinition{Metadata: toolshare.Info{
		Name:           t.record.Name,
		Description:    t.record.Description,
		Risk:           t.record.Risk,
		SuperadminOnly: t.record.SuperadminOnly,
		Tags:           t.record.Tags,
	}, LoadDetail: t.LoadDetail, ActivateTools: t.ActivateTools()}
}

func (d descriptor) LoadDetail(ctx context.Context) (toolshare.DetailBlock, error) {
	block, err := loadRecordDetail(d.record)
	if err != nil {
		return toolshare.DetailBlock{}, err
	}
	if d.record.Kind == KindAgent {
		block.Content = strings.TrimSpace(block.Content + "\n\n" + agentSkillNotice(ctx))
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

func agentSkillNotice(ctx context.Context) string {
	if actor, ok := contextinfo.ActorFromContext(ctx); ok && actor.Role == contextinfo.RoleSuperadmin {
		return "ElBot AgentSkill 使用提示：\n\n- 如该文档有脚本，请发现 agent_skill_creator，参考其说明是否把他注册成普通工具。"
	}
	return ""
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
