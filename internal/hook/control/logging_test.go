// 验证插件重载提交的日志结果与业务错误独立性。

package control

import (
	"testing"

	"elbot/internal/events"
	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/share"
)

type declaredReloadRuntime struct {
	fakeRuntime
	result events.LogResult
}

func (r *declaredReloadRuntime) ReplacePlugin(id string, config *share.RuntimeConfig, commit func()) (events.LogResult, error) {
	r.replaced = id
	commit()
	return r.result, nil
}

func TestPluginReloadCommitKeepsFailedResultSeparateFromBusiness(t *testing.T) {
	active := pipeline.NewManager(testLog, nil)
	registerPluginTestHook(t, active, "demo", "old")
	declared := events.LogResult{ResultStatus: events.ResultFailed, Content: "source reload facts"}
	runtime := &declaredReloadRuntime{result: declared}
	service := New(active, runtime, testLoader(active, func(candidate share.Registrar) (share.ReloadReport, []share.RuntimeConfig, error) {
		registerPluginTestHook(t, candidate, "demo", "new")
		return share.ReloadReport{}, []share.RuntimeConfig{{ID: "demo"}}, nil
	}))
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
