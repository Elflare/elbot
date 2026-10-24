// 验证插件重载提交的日志结果与业务错误独立性。

package control

import (
	"context"
	"testing"

	"elbot/internal/events"
	"elbot/internal/hook"
	hookruntime "elbot/internal/hook/runtime"
)

type declaredReloadRuntime struct {
	fakeRuntime
	result events.LogResult
}

func (r *declaredReloadRuntime) ReplacePlugin(id string, config *hookruntime.Config, commit func()) (events.LogResult, error) {
	r.replaced = id
	commit()
	return r.result, nil
}

func TestPluginReloadCommitKeepsFailedResultSeparateFromBusiness(t *testing.T) {
	active := hook.NewManager()
	active.SetWakeupFunc(func(context.Context, hook.Event) bool { return true })
	active.SetObserver(func(ctx context.Context, _ hook.Event, _ hook.ObserverInfo) (context.Context, func()) {
		return ctx, func() {}
	})
	registerPluginTestHook(t, active, "demo", "old")
	declared := events.LogResult{ResultStatus: events.ResultFailed, Content: "source reload facts"}
	runtime := &declaredReloadRuntime{result: declared}
	service := New(active, runtime, func(candidate hook.Registrar) (hook.ReloadReport, []hookruntime.Config, error) {
		registerPluginTestHook(t, candidate, "demo", "new")
		return hook.ReloadReport{}, []hookruntime.Config{{ID: "demo"}}, nil
	})
	commit, err := service.PreparePluginReload("demo")
	if err != nil {
		t.Fatal(err)
	}
	result, err := commit()
	if err != nil || result != declared || runtime.replaced != "demo" {
		t.Fatalf("result=%+v err=%v runtime=%+v", result, err, runtime)
	}
	assertHookNames(t, active.List(), "new")
}
