package app

import (
	"testing"

	elcron "elbot/internal/cron"
	"elbot/internal/hook"
)

func TestRegisterCronPlatformHookUsesDescription(t *testing.T) {
	manager := hook.NewManager()
	if err := registerCronPlatformHook(manager, &elcron.Service{}); err != nil {
		t.Fatalf("registerCronPlatformHook: %v", err)
	}
	infos := manager.List()
	if len(infos) != 1 {
		t.Fatalf("infos = %#v", infos)
	}
	if infos[0].Name != "builtin.cron.missed_once" || infos[0].Description == "" || infos[0].Detail != "" {
		t.Fatalf("info = %#v", infos[0])
	}
}
