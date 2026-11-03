// 验证插件重载提交的日志结果与业务错误独立性。

package control

import (
	"testing"

	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/share"
)

type declaredReloadRuntime struct {
	fakeRuntime
}

func (r *declaredReloadRuntime) ReplacePlugin(id string, config *share.RuntimeConfig, commit func()) error {
	r.replaced = id
	commit()
	return nil
}

func TestPluginReloadCommitsBusinessState(t *testing.T) {
	active := pipeline.NewManager(testLog, nil)
	registerPluginTestHook(t, active, "demo", "old")
	runtime := &declaredReloadRuntime{}
	service := New(active, runtime, testLoader(active, func(candidate share.Registrar) (share.ReloadReport, []share.RuntimeConfig, error) {
		registerPluginTestHook(t, candidate, "demo", "new")
		return share.ReloadReport{}, []share.RuntimeConfig{{ID: "demo"}}, nil
	}))
	commit, err := service.PreparePluginReload("demo")
	if err != nil {
		t.Fatal(err)
	}
	err = commit()
	if err != nil || runtime.replaced != "demo" {
		t.Fatal("result= err= runtime=", err, runtime)
	}
	assertHookNames(t, active.List(), "new")
}
