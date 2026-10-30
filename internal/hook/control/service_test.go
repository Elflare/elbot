// 验证 Hook 重载、规则替换和运行时管理行为。

package control

import (
	"context"
	"elbot/internal/events"
	"errors"
	"testing"

	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/share"
)

type fakeRuntime struct {
	infos     []share.RuntimeInfo
	applyErr  error
	applied   []share.RuntimeConfig
	started   string
	stopped   string
	restarted string
	replaced  string
}

func (r *fakeRuntime) Apply(configs []share.RuntimeConfig, commit func(string)) error {
	if r.applyErr != nil {
		return r.applyErr
	}
	r.applied = append([]share.RuntimeConfig(nil), configs...)
	for _, config := range configs {
		commit(config.ID)
	}
	return nil
}

func (r *fakeRuntime) ValidatePlugin(config share.RuntimeConfig) error {
	if r.applyErr != nil {
		return r.applyErr
	}
	return nil
}

func (r *fakeRuntime) ReplacePlugin(id string, config *share.RuntimeConfig, commit func()) (events.LogResult, error) {
	if r.applyErr != nil {
		return events.LogResult{ResultStatus: events.ResultFailed, Content: r.applyErr.Error()}, r.applyErr
	}
	r.replaced = id
	commit()
	return events.LogResult{ResultStatus: events.ResultSucceeded, Content: "replacement completed"}, nil
}

func (r *fakeRuntime) List() []share.RuntimeInfo { return r.infos }
func (r *fakeRuntime) Start(id string) error {
	r.started = id
	return nil
}
func (r *fakeRuntime) Stop(_ context.Context, id string) (bool, error) {
	r.stopped = id
	return true, nil
}
func (r *fakeRuntime) Restart(_ context.Context, id string) error {
	r.restarted = id
	return nil
}

func TestReloadKeepsActiveHooksWhenLoaderFails(t *testing.T) {
	active := pipeline.NewManager(testLog, nil)
	registerTestHook(t, active, "old")
	service := New(active, &fakeRuntime{}, testLoader(active, func(candidate share.Registrar) (share.ReloadReport, []share.RuntimeConfig, error) {
		registerTestHook(t, candidate, "new")
		return share.ReloadReport{Notices: []string{"warning"}}, nil, errors.New("load failed")
	}))

	report, err := service.HookReload()
	if err == nil || len(report.Notices) != 1 {
		t.Fatalf("HookReload report=%#v err=%v", report, err)
	}
	assertHookNames(t, active.List(), "old")
}

func TestReloadPublishesIndependentRulesWhenRuntimeApplyFails(t *testing.T) {
	active := pipeline.NewManager(testLog, nil)
	registerTestHook(t, active, "old")
	runtime := &fakeRuntime{applyErr: errors.New("old worker shutdown failed")}
	service := New(active, runtime, testLoader(active, func(candidate share.Registrar) (share.ReloadReport, []share.RuntimeConfig, error) {
		registerTestHook(t, candidate, "new")
		return share.ReloadReport{}, []share.RuntimeConfig{{ID: "demo"}}, nil
	}))

	if _, err := service.HookReload(); err == nil {
		t.Fatal("expected runtime apply error")
	}
	assertHookNames(t, active.List(), "new")
}

func TestReloadReplacesHooksAndAppliesRuntimeConfig(t *testing.T) {
	active := pipeline.NewManager(testLog, nil)
	registerTestHook(t, active, "old")
	runtime := &fakeRuntime{}
	service := New(active, runtime, testLoader(active, func(candidate share.Registrar) (share.ReloadReport, []share.RuntimeConfig, error) {
		registerTestHook(t, candidate, "new")
		return share.ReloadReport{}, []share.RuntimeConfig{{ID: "demo"}}, nil
	}))

	if _, err := service.HookReload(); err != nil {
		t.Fatalf("HookReload: %v", err)
	}
	assertHookNames(t, active.List(), "new")
	if len(runtime.applied) != 1 || runtime.applied[0].ID != "demo" {
		t.Fatalf("applied = %#v", runtime.applied)
	}
}

func TestServiceDelegatesStatefulLifecycle(t *testing.T) {
	runtime := &fakeRuntime{infos: []share.RuntimeInfo{{ID: "demo"}}}
	service := New(pipeline.NewManager(testLog, nil), runtime, nil)

	if infos := service.StatefulHooks(); len(infos) != 1 || infos[0].ID != "demo" {
		t.Fatalf("StatefulHooks = %#v", infos)
	}
	if err := service.StartStatefulHook("start"); err != nil {
		t.Fatal(err)
	}
	if matched, err := service.StopHook(context.Background(), "stop"); err != nil || !matched {
		t.Fatalf("StopHook = %v, %v", matched, err)
	}
	if err := service.RestartStatefulHook(context.Background(), "restart"); err != nil {
		t.Fatal(err)
	}
	if runtime.started != "start" || runtime.stopped != "stop" || runtime.restarted != "restart" {
		t.Fatalf("runtime calls = start:%q stop:%q restart:%q", runtime.started, runtime.stopped, runtime.restarted)
	}
}

func registerTestHook(t *testing.T, registrar share.Registrar, name string) {
	t.Helper()
	registrar.Register(share.Registration{
		Point: share.PointAgentInputPrepared,
		Name:  name,
		Match: share.Always(),
		Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, events.LogResult, error) {
			return event, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})
}

func assertHookNames(t *testing.T, infos []share.Info, want ...string) {
	t.Helper()
	if len(infos) != len(want) {
		t.Fatalf("hook infos = %#v, want %v", infos, want)
	}
	for i := range want {
		if infos[i].Name != want[i] {
			t.Fatalf("hook infos = %#v, want %v", infos, want)
		}
	}
}

func TestPreparePluginReloadReplacesOnlyRequestedPlugin(t *testing.T) {
	active := pipeline.NewManager(testLog, nil)
	registerPluginTestHook(t, active, "demo", "old")
	registerPluginTestHook(t, active, "other", "other")
	runtime := &fakeRuntime{}
	service := New(active, runtime, testLoader(active, func(candidate share.Registrar) (share.ReloadReport, []share.RuntimeConfig, error) {
		registerPluginTestHook(t, candidate, "demo", "new")
		registerPluginTestHook(t, candidate, "other", "other-new")
		return share.ReloadReport{}, []share.RuntimeConfig{{ID: "demo"}, {ID: "other"}}, nil
	}))

	commit, err := service.PreparePluginReload("demo")
	if err != nil {
		t.Fatalf("PreparePluginReload: %v", err)
	}
	assertHookNames(t, active.List(), "old", "other")
	if _, err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if runtime.replaced != "demo" {
		t.Fatalf("replaced runtime = %q, want demo", runtime.replaced)
	}
	infos := active.List()
	names := map[string]bool{}
	for _, info := range infos {
		names[info.Name] = true
	}
	if !names["new"] || !names["other"] || names["old"] || names["other-new"] {
		t.Fatalf("hook infos = %#v", infos)
	}
}

func registerPluginTestHook(t *testing.T, registrar share.Registrar, pluginID, name string) {
	t.Helper()
	registrar.Register(share.Registration{
		Point:    share.PointAgentInputPrepared,
		PluginID: pluginID,
		Name:     name,
		Match:    share.Always(),
		Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, events.LogResult, error) {
			return event, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})
}

func testLog(ctx context.Context, record events.LogRecord) {
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
}

// testLoader supplies the same candidate/commit contract as root wiring while
// letting each test choose the candidate registrations and load outcome.
func testLoader(active *pipeline.Manager, load func(share.Registrar) (share.ReloadReport, []share.RuntimeConfig, error)) Loader {
	return func() (share.ReloadReport, ReloadCandidate, error) {
		candidate := pipeline.NewManager(testLog, nil)
		report, configs, err := load(candidate)
		if err != nil {
			return report, ReloadCandidate{}, err
		}
		return report, ReloadCandidate{Infos: candidate.List(), Runtimes: configs, Commit: func(id string) { active.ReplacePlugin(id, candidate) }}, nil
	}
}

func TestPreparedReloadRetainsItsCandidateAcrossAnotherLoad(t *testing.T) {
	active := pipeline.NewManager(testLog, nil)
	registerPluginTestHook(t, active, "demo", "old")
	next := "first"
	service := New(active, &fakeRuntime{}, testLoader(active, func(candidate share.Registrar) (share.ReloadReport, []share.RuntimeConfig, error) {
		registerPluginTestHook(t, candidate, "demo", next)
		return share.ReloadReport{}, []share.RuntimeConfig{{ID: "demo"}}, nil
	}))
	first, err := service.PreparePluginReload("demo")
	if err != nil {
		t.Fatal(err)
	}
	next = "second"
	second, err := service.PreparePluginReload("demo")
	if err != nil {
		t.Fatal(err)
	}
	assertHookNames(t, active.List(), "old")
	if _, err := first(); err != nil {
		t.Fatal(err)
	}
	assertHookNames(t, active.List(), "first")
	if _, err := second(); err != nil {
		t.Fatal(err)
	}
	assertHookNames(t, active.List(), "second")
}
