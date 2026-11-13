package control

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"strings"
)

func (m *Supervisor) ListHooks(ctx context.Context) (operationresult.Result[ListHooksResult], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[ListHooksResult]{}, err
	}
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || actor.Role != contextinfo.RoleSuperadmin {
		return operationresult.Result[ListHooksResult]{Status: operationresult.Forbidden, Message: "无权执行该操作"}, nil
	}
	items, err := m.deps.Hooks.List(ctx)
	if items == nil {
		items = []HookInfo{}
	}
	if err != nil {
		return operationresult.Result[ListHooksResult]{}, err
	}
	return operationresult.Result[ListHooksResult]{Status: operationresult.Success, Data: ListHooksResult{Items: items}}, nil
}

func (m *Supervisor) GetHook(ctx context.Context, req GetHookRequest) (operationresult.Result[GetHookResult], error) {
	result := GetHookResult{Items: []HookInfo{}}
	if err := ctx.Err(); err != nil {
		return operationresult.Result[GetHookResult]{}, err
	}
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || actor.Role != contextinfo.RoleSuperadmin {
		return operationresult.Result[GetHookResult]{Status: operationresult.Forbidden, Message: "无权执行该操作"}, nil
	}
	if strings.TrimSpace(req.HookID) == "" {
		return operationresult.Result[GetHookResult]{Status: operationresult.InvalidArguments, Message: "hook_id 不能为空"}, nil
	}
	items, err := m.deps.Hooks.List(ctx)
	if err != nil {
		return operationresult.Result[GetHookResult]{}, err
	}
	for _, item := range items {
		if item.ID == req.HookID || item.Name == req.HookID {
			result.Items = append(result.Items, item)
		}
	}
	if len(result.Items) == 0 {
		return operationresult.Result[GetHookResult]{Status: operationresult.NotFound, Message: "Hook 不存在"}, nil
	}
	return operationresult.Result[GetHookResult]{Status: operationresult.Success, Data: result}, nil
}

func (m *Supervisor) ReloadHooks(ctx context.Context) (operationresult.Result[ReloadHooksResult], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[ReloadHooksResult]{}, err
	}
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || actor.Role != contextinfo.RoleSuperadmin {
		return operationresult.Result[ReloadHooksResult]{Status: operationresult.Forbidden, Message: "无权执行该操作"}, nil
	}
	result, err := m.deps.Hooks.Reload(ctx)
	if err != nil {
		return operationresult.Result[ReloadHooksResult]{}, err
	}
	return operationresult.Result[ReloadHooksResult]{Status: operationresult.Success, Data: result}, nil
}

func (m *Supervisor) StartHook(ctx context.Context, req StartHookRequest) (operationresult.Result[struct{}], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || actor.Role != contextinfo.RoleSuperadmin {
		return operationresult.Result[struct{}]{Status: operationresult.Forbidden, Message: "无权执行该操作"}, nil
	}
	if strings.TrimSpace(req.HookID) == "" {
		return operationresult.Result[struct{}]{Status: operationresult.InvalidArguments, Message: "hook_id 不能为空"}, nil
	}
	return m.deps.Hooks.Start(ctx, req.HookID)
}

func (m *Supervisor) StopHook(ctx context.Context, req StopHookRequest) (operationresult.Result[struct{}], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || actor.Role != contextinfo.RoleSuperadmin {
		return operationresult.Result[struct{}]{Status: operationresult.Forbidden, Message: "无权执行该操作"}, nil
	}
	if strings.TrimSpace(req.HookID) == "" {
		return operationresult.Result[struct{}]{Status: operationresult.InvalidArguments, Message: "hook_id 不能为空"}, nil
	}
	return m.deps.Hooks.Stop(ctx, req.HookID)
}

func (m *Supervisor) RestartHook(ctx context.Context, req RestartHookRequest) (operationresult.Result[struct{}], error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || actor.Role != contextinfo.RoleSuperadmin {
		return operationresult.Result[struct{}]{Status: operationresult.Forbidden, Message: "无权执行该操作"}, nil
	}
	if strings.TrimSpace(req.HookID) == "" {
		return operationresult.Result[struct{}]{Status: operationresult.InvalidArguments, Message: "hook_id 不能为空"}, nil
	}
	return m.deps.Hooks.Restart(ctx, req.HookID)
}
