package hook

import (
	"context"
	"elbot/internal/operationresult"
	"slices"
	"strings"

	"elbot/internal/control"
)

// management 将根部准入与内部管理能力接到统一操作，不暴露运行组件。
type management struct{ supervisor *Supervisor }

func (a management) List(ctx context.Context) ([]control.HookInfo, error) {
	_, done, err := a.supervisor.beginCall(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	items := []control.HookInfo{}
	for _, info := range a.supervisor.control.HookList() {
		items = append(items, control.HookInfo{ID: info.PluginID, Name: info.Name, Description: info.Description, Point: string(info.Point), Priority: info.Priority, Detail: info.Detail, Active: info.Active})
	}
	for _, info := range a.supervisor.control.StatefulHooks() {
		items = append(items, control.HookInfo{ID: info.ID, Description: info.Description, Mode: info.Mode, Status: info.Status, Detail: info.Detail, Active: info.Active, Waiting: info.Waiting})
	}
	slices.SortStableFunc(items, func(a, b control.HookInfo) int {
		if n := strings.Compare(a.ID, b.ID); n != 0 {
			return n
		}
		return strings.Compare(a.Name, b.Name)
	})
	return items, nil
}
func (a management) Reload(ctx context.Context) (control.ReloadHooksResult, error) {
	_, done, err := a.supervisor.beginCall(ctx)
	if err != nil {
		return control.ReloadHooksResult{}, err
	}
	defer done()
	report, err := a.supervisor.control.HookReload()
	return control.ReloadHooksResult{Notices: slices.Clone(report.Notices)}, err
}
func (a management) Start(ctx context.Context, id string) (operationresult.Result[struct{}], error) {
	_, done, err := a.supervisor.beginCall(ctx)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	defer done()
	return a.supervisor.control.StartStatefulHook(id)
}
func (a management) Stop(ctx context.Context, id string) (operationresult.Result[struct{}], error) {
	ctx, done, err := a.supervisor.beginCall(ctx)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	defer done()
	return a.supervisor.control.StopHook(ctx, id)
}
func (a management) Restart(ctx context.Context, id string) (operationresult.Result[struct{}], error) {
	ctx, done, err := a.supervisor.beginCall(ctx)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	defer done()
	return a.supervisor.control.RestartStatefulHook(ctx, id)
}
