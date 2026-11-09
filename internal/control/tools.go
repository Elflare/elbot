package control

import (
	"context"
	"strings"

	"elbot/internal/tool"
)

func (m *Supervisor) QueryTools(ctx context.Context, req tool.ToolQuery) ([]tool.ToolInfo, error) {
	if err := authorize(ctx, true, m.deps.Tools != nil); err != nil {
		return nil, err
	}
	items, err := m.deps.Tools.QueryTools(ctx, req)
	return items, ClassifyError(err)
}

func (m *Supervisor) LoadToolDetail(ctx context.Context, req ToolDetailRequest) (tool.DetailBlock, error) {
	if err := authorize(ctx, true, m.deps.Tools != nil); err != nil {
		return tool.DetailBlock{}, err
	}
	if strings.TrimSpace(req.Name) == "" {
		return tool.DetailBlock{}, invalid("name 不能为空")
	}
	block, err := m.deps.Tools.LoadToolDetail(ctx, req.Name)
	return block, ClassifyError(err)
}
func (m *Supervisor) ReloadSkills(ctx context.Context) error {
	if err := authorize(ctx, true, m.deps.Skills != nil); err != nil {
		return err
	}
	return ClassifyError(m.deps.Skills.Reload(ctx))
}
func (m *Supervisor) RemoveSkill(ctx context.Context, req RemoveSkillRequest) error {
	if err := authorize(ctx, true, m.deps.Skills != nil); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) == "" {
		return invalid("name 不能为空")
	}
	if !req.Confirmed {
		return &Error{Code: "confirmation_required", Message: "删除技能需要确认"}
	}
	return ClassifyError(m.deps.Skills.Remove(ctx, req.Name))
}
