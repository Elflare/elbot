package control

import (
	"testing"

	"elbot/internal/events"
	"elbot/internal/hook"
	hookruntime "elbot/internal/hook/runtime"
)

type declaredReloadRuntime struct {
	fakeRuntime
	result events.LogResult
}

func (r *declaredReloadRuntime) ReplacePlugin(config hookruntime.Config) (events.LogResult, error) {
	r.replaced = config.ID
	return r.result, nil
}

func TestPluginReloadCommitKeepsFailedResultSeparateFromBusiness(t *testing.T) {
	active := hook.NewManager()
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
