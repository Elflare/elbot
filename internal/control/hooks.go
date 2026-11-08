package control

import (
	"context"
	"strings"
)

func (m *Supervisor) ListHooks(ctx context.Context) (ListHooksResult, error) {
	if err := authorize(ctx, true, m.deps.Hooks != nil); err != nil {
		return ListHooksResult{}, err
	}
	items, err := m.deps.Hooks.List(ctx)
	if items == nil {
		items = []HookInfo{}
	}
	return ListHooksResult{Items: items}, ClassifyError(err)
}

func (m *Supervisor) GetHook(ctx context.Context, req GetHookRequest) (GetHookResult, error) {
	result := GetHookResult{Items: []HookInfo{}}
	if err := authorize(ctx, true, m.deps.Hooks != nil); err != nil {
		return result, err
	}
	if strings.TrimSpace(req.HookID) == "" {
		return result, invalid("hook_id 不能为空")
	}
	items, err := m.deps.Hooks.List(ctx)
	if err != nil {
		return result, ClassifyError(err)
	}
	for _, item := range items {
		if item.ID == req.HookID || item.Name == req.HookID {
			result.Items = append(result.Items, item)
		}
	}
	if len(result.Items) == 0 {
		return result, &Error{Code: "not_found", Message: "Hook 不存在"}
	}
	return result, nil
}

func (m *Supervisor) ReloadHooks(ctx context.Context) (ReloadHooksResult, error) {
	if err := authorize(ctx, true, m.deps.Hooks != nil); err != nil {
		return ReloadHooksResult{}, err
	}
	result, err := m.deps.Hooks.Reload(ctx)
	return result, ClassifyError(err)
}

func (m *Supervisor) StartHook(ctx context.Context, req StartHookRequest) error {
	if err := authorize(ctx, true, m.deps.Hooks != nil); err != nil {
		return err
	}
	if strings.TrimSpace(req.HookID) == "" {
		return invalid("hook_id 不能为空")
	}
	return ClassifyError(m.deps.Hooks.Start(ctx, req.HookID))
}

func (m *Supervisor) StopHook(ctx context.Context, req StopHookRequest) error {
	if err := authorize(ctx, true, m.deps.Hooks != nil); err != nil {
		return err
	}
	if strings.TrimSpace(req.HookID) == "" {
		return invalid("hook_id 不能为空")
	}
	return ClassifyError(m.deps.Hooks.Stop(ctx, req.HookID))
}

func (m *Supervisor) RestartHook(ctx context.Context, req RestartHookRequest) error {
	if err := authorize(ctx, true, m.deps.Hooks != nil); err != nil {
		return err
	}
	if strings.TrimSpace(req.HookID) == "" {
		return invalid("hook_id 不能为空")
	}
	return ClassifyError(m.deps.Hooks.Restart(ctx, req.HookID))
}
