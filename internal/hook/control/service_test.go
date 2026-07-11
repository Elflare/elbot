package control

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/hook"
	hookruntime "elbot/internal/hook/runtime"
)

type fakeRuntime struct {
	infos     []hookruntime.Info
	applyErr  error
	applied   []hookruntime.Config
	started   string
	stopped   string
	restarted string
	replaced  string
}

func (r *fakeRuntime) Apply(configs []hookruntime.Config) error {
	if r.applyErr != nil {
		return r.applyErr
	}
	r.applied = append([]hookruntime.Config(nil), configs...)
	return nil
}

func (r *fakeRuntime) ValidatePlugin(config hookruntime.Config) error {
	if r.applyErr != nil {
		return r.applyErr
	}
	return nil
}

func (r *fakeRuntime) ReplacePlugin(config hookruntime.Config) error {
	if r.applyErr != nil {
		return r.applyErr
	}
	r.replaced = config.ID
	return nil
}

func (r *fakeRuntime) List() []hookruntime.Info { return r.infos }
func (r *fakeRuntime) Start(id string) error {
	r.started = id
	return nil
}
func (r *fakeRuntime) Stop(_ context.Context, id string) error {
	r.stopped = id
	return nil
}
func (r *fakeRuntime) Restart(_ context.Context, id string) error {
	r.restarted = id
	return nil
}

func TestReloadKeepsActiveHooksWhenLoaderFails(t *testing.T) {
	active := hook.NewManager()
	registerTestHook(t, active, "old")
	service := New(active, &fakeRuntime{}, func(candidate hook.Registrar) (hook.ReloadReport, []hookruntime.Config, error) {
		registerTestHook(t, candidate, "new")
		return hook.ReloadReport{Notices: []string{"warning"}}, nil, errors.New("load failed")
	})

	report, err := service.HookReload()
	if err == nil || len(report.Notices) != 1 {
		t.Fatalf("HookReload report=%#v err=%v", report, err)
	}
	assertHookNames(t, active.List(), "old")
}

func TestReloadKeepsActiveHooksWhenRuntimeApplyFails(t *testing.T) {
	active := hook.NewManager()
	registerTestHook(t, active, "old")
	runtime := &fakeRuntime{applyErr: errors.New("invalid runtime")}
	service := New(active, runtime, func(candidate hook.Registrar) (hook.ReloadReport, []hookruntime.Config, error) {
		registerTestHook(t, candidate, "new")
		return hook.ReloadReport{}, []hookruntime.Config{{ID: "demo"}}, nil
	})

	if _, err := service.HookReload(); err == nil {
		t.Fatal("expected runtime apply error")
	}
	assertHookNames(t, active.List(), "old")
}

func TestReloadReplacesHooksAndAppliesRuntimeConfig(t *testing.T) {
	active := hook.NewManager()
	registerTestHook(t, active, "old")
	runtime := &fakeRuntime{}
	service := New(active, runtime, func(candidate hook.Registrar) (hook.ReloadReport, []hookruntime.Config, error) {
		registerTestHook(t, candidate, "new")
		return hook.ReloadReport{}, []hookruntime.Config{{ID: "demo"}}, nil
	})

	if _, err := service.HookReload(); err != nil {
		t.Fatalf("HookReload: %v", err)
	}
	assertHookNames(t, active.List(), "new")
	if len(runtime.applied) != 1 || runtime.applied[0].ID != "demo" {
		t.Fatalf("applied = %#v", runtime.applied)
	}
}

func TestServiceDelegatesStatefulLifecycle(t *testing.T) {
	runtime := &fakeRuntime{infos: []hookruntime.Info{{ID: "demo"}}}
	service := New(hook.NewManager(), runtime, nil)

	if infos := service.StatefulHooks(); len(infos) != 1 || infos[0].ID != "demo" {
		t.Fatalf("StatefulHooks = %#v", infos)
	}
	if err := service.StartStatefulHook("start"); err != nil {
		t.Fatal(err)
	}
	if err := service.StopStatefulHook(context.Background(), "stop"); err != nil {
		t.Fatal(err)
	}
	if err := service.RestartStatefulHook(context.Background(), "restart"); err != nil {
		t.Fatal(err)
	}
	if runtime.started != "start" || runtime.stopped != "stop" || runtime.restarted != "restart" {
		t.Fatalf("runtime calls = start:%q stop:%q restart:%q", runtime.started, runtime.stopped, runtime.restarted)
	}
}

func registerTestHook(t *testing.T, registrar hook.Registrar, name string) {
	t.Helper()
	if err := registrar.Register(hook.Registration{
		Point:   hook.PointAgentInputPrepared,
		Name:    name,
		Match:   hook.Always(),
		Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, error) { return event, nil }),
	}); err != nil {
		t.Fatalf("Register %s: %v", name, err)
	}
}

func assertHookNames(t *testing.T, infos []hook.Info, want ...string) {
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

func TestServiceReportsMissingRuntime(t *testing.T) {
	service := New(hook.NewManager(), nil, func(hook.Registrar) (hook.ReloadReport, []hookruntime.Config, error) {
		return hook.ReloadReport{}, []hookruntime.Config{{ID: "demo"}}, nil
	})
	if _, err := service.HookReload(); err == nil {
		t.Fatal("expected missing runtime error")
	}
	if err := service.StartStatefulHook("demo"); err == nil {
		t.Fatal("expected missing runtime start error")
	}
}

func TestPreparePluginReloadReplacesOnlyRequestedPlugin(t *testing.T) {
	active := hook.NewManager()
	registerPluginTestHook(t, active, "demo", "old")
	registerPluginTestHook(t, active, "other", "other")
	runtime := &fakeRuntime{}
	service := New(active, runtime, func(candidate hook.Registrar) (hook.ReloadReport, []hookruntime.Config, error) {
		registerPluginTestHook(t, candidate, "demo", "new")
		registerPluginTestHook(t, candidate, "other", "other-new")
		return hook.ReloadReport{}, []hookruntime.Config{{ID: "demo"}, {ID: "other"}}, nil
	})

	commit, err := service.PreparePluginReload("demo")
	if err != nil {
		t.Fatalf("PreparePluginReload: %v", err)
	}
	assertHookNames(t, active.List(), "old", "other")
	if err := commit(); err != nil {
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

func registerPluginTestHook(t *testing.T, registrar hook.Registrar, pluginID, name string) {
	t.Helper()
	if err := registrar.Register(hook.Registration{
		Point:    hook.PointAgentInputPrepared,
		PluginID: pluginID,
		Name:     name,
		Match:    hook.Always(),
		Handler:  hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, error) { return event, nil }),
	}); err != nil {
		t.Fatalf("Register %s: %v", name, err)
	}
}
