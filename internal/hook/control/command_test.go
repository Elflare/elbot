package control

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/command"
	"elbot/internal/hook/share"
)

type fakeHookService struct {
	infos        []share.Info
	runtimeInfos []share.RuntimeInfo
	reloadReport share.ReloadReport
	started      string
	stopped      string
	restarted    string
}

func (s *fakeHookService) HookList() []share.Info { return s.infos }
func (s *fakeHookService) HookReload() (share.ReloadReport, error) {
	return s.reloadReport, nil
}
func (s *fakeHookService) StatefulHooks() []share.RuntimeInfo { return s.runtimeInfos }
func (s *fakeHookService) StartStatefulHook(id string) error {
	s.started = id
	return nil
}
func (s *fakeHookService) StopHook(_ context.Context, id string) (bool, error) {
	s.stopped = id
	return true, nil
}
func (s *fakeHookService) RestartStatefulHook(_ context.Context, id string) error {
	s.restarted = id
	return nil
}

func TestHookListPreservesDescriptionsAndActivity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		infos    []share.Info
		runtimes []share.RuntimeInfo
		want     string
	}{
		{name: "empty", want: "hooks: none"},
		{name: "standalone", infos: []share.Info{{Name: "rule", Point: share.PointPlatformConnected, Priority: 3, Description: "  description  ", Active: 2}}, want: "hooks:\n  rule  [platform.connected]  priority=3 - description | 2 actived"},
		{name: "plugin sum", infos: []share.Info{{PluginID: "plugin", Active: 2}, {PluginID: "plugin", Description: " fallback ", Active: 3}}, want: "hooks:\n  plugin  [plugin]  rules=2 - fallback | 5 actived"},
		{name: "runtime precedence", infos: []share.Info{{PluginID: "plugin", Description: "rule", Active: 9}}, runtimes: []share.RuntimeInfo{{ID: "plugin", Mode: "persistent", Status: "ready", Description: " runtime ", Active: 2, Waiting: 1}}, want: "hooks:\n  plugin  [persistent:ready]  rules=1 - runtime | 2 actived | 1 waiting"},
		{name: "runtime zero", infos: []share.Info{{PluginID: "plugin", Description: " fallback ", Active: 9}}, runtimes: []share.RuntimeInfo{{ID: "plugin", Mode: "persistent", Status: "ready", Description: " "}}, want: "hooks:\n  plugin  [persistent:ready]  rules=1 - fallback"},
		{name: "separate runtimes", runtimes: []share.RuntimeInfo{{ID: "b", Mode: "transient", Status: "stopped"}, {ID: "a", Mode: "persistent", Status: "ready", Waiting: 1}}, want: "hooks:\n  a  [persistent:ready] | 1 waiting\n  b  [transient:stopped]"},
		{name: "unicode truncation", infos: []share.Info{{Name: "rule", Description: " " + strings.Repeat("雪", 61) + " "}}, want: "hooks:\n  rule  []  priority=0 - " + strings.Repeat("雪", 60) + "..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := formatHookList(&fakeHookService{infos: tc.infos, runtimeInfos: tc.runtimes})
			if got != tc.want {
				t.Fatalf("list = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHooksCommandShowsDescriptionsAndDetails(t *testing.T) {
	longDescription := "123456789012345678901234567890123456789012345678901234567890tail"
	hooks := &fakeHookService{infos: []share.Info{
		{
			Name:        "greet",
			Description: longDescription,
			Point:       share.PointPlatformMessageReceived,
			Priority:    1000,
			Detail:      "on: platform.message.received\nmatch: always",
		},
	}}
	cmd := NewHooks(hooks, testBeginCall)

	result, err := cmd.Handle(context.Background(), command.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "greet") || !strings.Contains(result.Content, "123456789012345678901234567890123456789012345678901234567890...") || strings.Contains(result.Content, "tail") {
		t.Fatalf("list content = %q", result.Content)
	}

	result, err = cmd.Handle(context.Background(), command.Request{Args: "greet"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "name: greet") || !strings.Contains(result.Content, "description: "+longDescription) {
		t.Fatalf("detail content = %q", result.Content)
	}
	if strings.Count(result.Content, "description:") != 1 {
		t.Fatalf("description duplicated in %q", result.Content)
	}
	if !strings.Contains(result.Content, "priority: 1000\ndescription: "+longDescription+"\non: platform.message.received") || strings.Contains(result.Content, "\n\n") {
		t.Fatalf("detail fields are not contiguous in %q", result.Content)
	}
}

func TestHooksCommandCompletesRuleName(t *testing.T) {
	hooks := &fakeHookService{infos: []share.Info{{Name: "greet", Point: share.PointPlatformMessageReceived}}}
	cmd := NewHooks(hooks, testBeginCall).(command.Completer)

	got := cmd.Complete(context.Background(), command.CompletionRequest{Raw: "/hooks g", Prefix: "/", Name: "hooks", Args: "g", Cursor: len("/hooks g")})
	if len(got) != 1 || got[0].Text != "greet" {
		t.Fatalf("Complete = %#v", got)
	}
}

func TestHooksCommandGroupsPluginRulesAndExpandsDetail(t *testing.T) {
	hooks := &fakeHookService{
		infos: []share.Info{
			{PluginID: "weather", Name: "forecast", Description: "weather plugin", Point: share.PointPlatformMessageReceived, Priority: 1000, Detail: "on: platform.message.received\nmatch: forecast"},
			{PluginID: "weather", Name: "weather_help", Description: "weather plugin", Point: share.PointAgentInputPrepared, Priority: 900, Detail: "on: agent.input.prepared\nmatch: help", Active: 2},
			{Name: "builtin.cron.missed_once", Description: "cron fallback", Point: share.PointPlatformConnected},
		},
		runtimeInfos: []share.RuntimeInfo{{ID: "weather", Description: "weather plugin", Mode: "persistent", Status: "ready", Active: 1, Waiting: 1}},
	}
	cmd := NewHooks(hooks, testBeginCall)

	result, err := cmd.Handle(context.Background(), command.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(result.Content, "\n  weather  ") != 1 || !strings.Contains(result.Content, "weather  [persistent:ready]  rules=2") {
		t.Fatalf("grouped list = %q", result.Content)
	}
	if strings.Contains(result.Content, "forecast") || strings.Contains(result.Content, "weather_help") {
		t.Fatalf("plugin rules leaked into list: %q", result.Content)
	}
	if !strings.Contains(result.Content, "builtin.cron.missed_once  [platform.connected]") {
		t.Fatalf("standalone hook missing from list: %q", result.Content)
	}

	result, err = cmd.Handle(context.Background(), command.Request{Args: "weather"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mode: persistent\nstatus: ready", "rules: 2", "rule: forecast", "rule: weather_help", "match: forecast", "match: help"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("plugin detail missing %q: %q", want, result.Content)
		}
	}
	if strings.Count(result.Content, "description: weather plugin") != 1 {
		t.Fatalf("plugin description duplicated in %q", result.Content)
	}

	result, err = cmd.Handle(context.Background(), command.Request{Args: "forecast"})
	if err != nil || !strings.Contains(result.Content, "name: forecast\npoint: platform.message.received") {
		t.Fatalf("direct rule detail = %#v, %v", result, err)
	}

	completer := cmd.(command.Completer)
	got := completer.Complete(context.Background(), command.CompletionRequest{Raw: "/hooks w", Prefix: "/", Name: "hooks", Args: "w", Cursor: len("/hooks w")})
	if len(got) != 1 || got[0].Text != "weather" || got[0].Description != "2 rules, persistent:ready" {
		t.Fatalf("grouped completion = %#v", got)
	}
}

func TestHooksReloadShowsWarnings(t *testing.T) {
	hooks := &fakeHookService{reloadReport: share.ReloadReport{Notices: []string{"Hook 插件 gpt_image 已跳过：bad field"}}}
	cmd := NewHooks(hooks, testBeginCall)

	result, err := cmd.Handle(context.Background(), command.Request{Args: "reload"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "hook reload completed with warnings") || !strings.Contains(result.Content, "gpt_image") {
		t.Fatalf("reload content = %q", result.Content)
	}
}

func TestHooksCommandManagesStatefulHooks(t *testing.T) {
	hooks := &fakeHookService{runtimeInfos: []share.RuntimeInfo{{ID: "weather", Description: "weather loop", Mode: "persistent", Status: "ready"}}}
	cmd := NewHooks(hooks, testBeginCall)

	result, err := cmd.Handle(context.Background(), command.Request{})
	if err != nil || !strings.Contains(result.Content, "weather  [persistent:ready]") {
		t.Fatalf("list = %#v, %v", result, err)
	}
	result, err = cmd.Handle(context.Background(), command.Request{Args: "weather"})
	if err != nil || !strings.Contains(result.Content, "mode: persistent\nstatus: ready") {
		t.Fatalf("detail = %#v, %v", result, err)
	}
	result, err = cmd.Handle(context.Background(), command.Request{Args: "restart weather"})
	if err != nil || hooks.restarted != "weather" || !strings.Contains(result.Content, "completed") {
		t.Fatalf("restart = %#v, restarted=%q, err=%v", result, hooks.restarted, err)
	}
}

func testBeginCall(ctx context.Context) (context.Context, func(), error) { return ctx, func() {}, nil }
