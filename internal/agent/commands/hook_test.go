package commands

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/command"
	"elbot/internal/hook"
	hookruntime "elbot/internal/hook/runtime"
)

type fakeHookService struct {
	infos        []hook.Info
	runtimeInfos []hookruntime.Info
	reloadReport hook.ReloadReport
	started      string
	stopped      string
	restarted    string
}

func (s *fakeHookService) HookList() []hook.Info { return s.infos }
func (s *fakeHookService) HookReload() (hook.ReloadReport, error) {
	return s.reloadReport, nil
}
func (s *fakeHookService) StatefulHooks() []hookruntime.Info { return s.runtimeInfos }
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

func TestHooksCommandShowsDescriptionsAndDetails(t *testing.T) {
	longDescription := "123456789012345678901234567890123456789012345678901234567890tail"
	hooks := &fakeHookService{infos: []hook.Info{
		{
			Name:        "greet",
			Description: longDescription,
			Point:       hook.PointPlatformMessageReceived,
			Priority:    1000,
			Detail:      "on: platform.message.received\nmatch: always",
		},
	}}
	cmd := NewHooks(Deps{Hooks: hooks})

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
	if !strings.Contains(result.Content, "name: greet") || !strings.Contains(result.Content, "description: "+longDescription) || strings.Contains(result.Content, "rules.greet") {
		t.Fatalf("detail content = %q", result.Content)
	}
	if strings.Count(result.Content, "description:") != 1 {
		t.Fatalf("description duplicated in %q", result.Content)
	}
	if !strings.Contains(result.Content, "priority: 1000\ndescription: "+longDescription+"\non: platform.message.received") || strings.Contains(result.Content, "\n\n") {
		t.Fatalf("detail fields are not contiguous in %q", result.Content)
	}
}

func TestHooksCommandCompletesRuleNameWithoutRulesPrefix(t *testing.T) {
	hooks := &fakeHookService{infos: []hook.Info{{Name: "greet", Point: hook.PointPlatformMessageReceived}}}
	cmd := NewHooks(Deps{Hooks: hooks}).(command.Completer)

	got := cmd.Complete(context.Background(), command.CompletionRequest{Raw: "/hooks g", Prefix: "/", Name: "hooks", Args: "g", Cursor: len("/hooks g")})
	if len(got) != 1 || got[0].Text != "greet" || strings.HasPrefix(got[0].Text, "rules.") {
		t.Fatalf("Complete = %#v", got)
	}
}

func TestHooksReloadShowsWarnings(t *testing.T) {
	hooks := &fakeHookService{reloadReport: hook.ReloadReport{Notices: []string{"Hook 插件 gpt_image 已跳过：bad field"}}}
	cmd := NewHooks(Deps{Hooks: hooks})

	result, err := cmd.Handle(context.Background(), command.Request{Args: "reload"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "hook reload completed with warnings") || !strings.Contains(result.Content, "gpt_image") {
		t.Fatalf("reload content = %q", result.Content)
	}
}

func TestHooksCommandManagesStatefulHooks(t *testing.T) {
	hooks := &fakeHookService{runtimeInfos: []hookruntime.Info{{ID: "weather", Description: "weather loop", Mode: hookruntime.ModePersistent, Status: hookruntime.StatusReady}}}
	cmd := NewHooks(Deps{Hooks: hooks})

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
