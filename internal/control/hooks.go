package control

import (
	"context"
	"elbot/internal/control/share"
	"elbot/internal/operationresult"
	"strings"
)

func (m *Supervisor) ListHooks(ctx context.Context) (operationresult.Result[ListHooksResult], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[ListHooksResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[ListHooksResult]{Status: authorized.Status, Message: authorized.Message}, nil
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
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[GetHookResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[GetHookResult]{Status: authorized.Status, Message: authorized.Message}, nil
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
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[ReloadHooksResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[ReloadHooksResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	result, err := m.deps.Hooks.Reload(ctx)
	if err != nil {
		return operationresult.Result[ReloadHooksResult]{}, err
	}
	return operationresult.Result[ReloadHooksResult]{Status: operationresult.Success, Data: result}, nil
}

func (m *Supervisor) StartHook(ctx context.Context, req StartHookRequest) (operationresult.Result[struct{}], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[struct{}]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if strings.TrimSpace(req.HookID) == "" {
		return operationresult.Result[struct{}]{Status: operationresult.InvalidArguments, Message: "hook_id 不能为空"}, nil
	}
	return m.deps.Hooks.Start(ctx, req.HookID)
}

func (m *Supervisor) StopHook(ctx context.Context, req StopHookRequest) (operationresult.Result[struct{}], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[struct{}]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if strings.TrimSpace(req.HookID) == "" {
		return operationresult.Result[struct{}]{Status: operationresult.InvalidArguments, Message: "hook_id 不能为空"}, nil
	}
	return m.deps.Hooks.Stop(ctx, req.HookID)
}

func (m *Supervisor) RestartHook(ctx context.Context, req RestartHookRequest) (operationresult.Result[struct{}], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[struct{}]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	if strings.TrimSpace(req.HookID) == "" {
		return operationresult.Result[struct{}]{Status: operationresult.InvalidArguments, Message: "hook_id 不能为空"}, nil
	}
	return m.deps.Hooks.Restart(ctx, req.HookID)
}
