package control

import (
	"context"
	"strings"

	"elbot/internal/control/share"
	"elbot/internal/operationresult"
	"elbot/internal/tool"
)

func (m *Supervisor) QueryTools(ctx context.Context, req tool.ToolQuery) (operationresult.Result[[]tool.ToolInfo], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[[]tool.ToolInfo]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[[]tool.ToolInfo]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	items, err := m.deps.Tools.QueryTools(ctx, req)
	if err != nil {
		return operationresult.Result[[]tool.ToolInfo]{}, err
	}
	return operationresult.Result[[]tool.ToolInfo]{Status: operationresult.Success, Data: items}, nil
}

func (m *Supervisor) LoadToolDetail(ctx context.Context, req ToolDetailRequest) (operationresult.Result[tool.DetailBlock], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[tool.DetailBlock]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[tool.DetailBlock]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if strings.TrimSpace(req.Name) == "" {
		return operationresult.Result[tool.DetailBlock]{Status: operationresult.InvalidArguments, Message: "name 不能为空"}, nil
	}
	return m.deps.Tools.LoadToolDetail(ctx, req.Name)
}

func (m *Supervisor) ReloadSkills(ctx context.Context) (operationresult.Result[struct{}], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[struct{}]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if err := m.deps.Skills.Reload(ctx); err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (m *Supervisor) RemoveSkill(ctx context.Context, req RemoveSkillRequest) (operationresult.Result[struct{}], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[struct{}]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if strings.TrimSpace(req.Name) == "" {
		return operationresult.Result[struct{}]{Status: operationresult.InvalidArguments, Message: "name 不能为空"}, nil
	}
	if !req.Confirmed {
		return operationresult.Result[struct{}]{Status: operationresult.Rejected, Message: "删除技能需要确认"}, nil
	}
	return m.deps.Skills.Remove(ctx, req.Name)
}
