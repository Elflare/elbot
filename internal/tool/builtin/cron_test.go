package builtin

import (
	"context"
	"strings"
	"testing"
	"time"

	elcron "elbot/internal/cron"
	"elbot/internal/tool"
)

func TestCronToolCallReturnsCurrentTimeHint(t *testing.T) {
	result, err := (CronTool{}).Call(context.Background(), tool.CallRequest{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(result.Content, "当前本地时间") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestResolveRunAtUsesRelativeOffsets(t *testing.T) {
	got, err := resolveRunAt("", relativeOffset{Minutes: 10})
	if err != nil {
		t.Fatalf("resolveRunAt: %v", err)
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", got, time.Local)
	if err != nil {
		t.Fatalf("parse resolved run_at: %v", err)
	}
	if d := time.Until(parsed); d < 9*time.Minute || d > 11*time.Minute {
		t.Fatalf("resolved duration = %s, run_at=%s", d, got)
	}
}

func TestResolveRunAtUsesWeeksAndMonths(t *testing.T) {
	got, err := resolveRunAt("", relativeOffset{Weeks: 1, Months: 1})
	if err != nil {
		t.Fatalf("resolveRunAt: %v", err)
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", got, time.Local)
	if err != nil {
		t.Fatalf("parse resolved run_at: %v", err)
	}
	if !parsed.After(time.Now().AddDate(0, 0, 6)) {
		t.Fatalf("resolved run_at too early: %s", got)
	}
}

func TestResolveRunAtRejectsAbsoluteAndRelativeTogether(t *testing.T) {
	_, err := resolveRunAt("2026-01-02 03:04:05", relativeOffset{Minutes: 1})
	if err == nil || !strings.Contains(err.Error(), "不能和 run_after_*") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveUpdateRunAtReturnsPointerForRelativeOffset(t *testing.T) {
	got, err := resolveUpdateRunAt(nil, relativeOffset{Hours: 1})
	if err != nil {
		t.Fatalf("resolveUpdateRunAt: %v", err)
	}
	if got == nil || *got == "" {
		t.Fatalf("run_at pointer = %#v", got)
	}
}

func TestCronToolsRiskAndVisibility(t *testing.T) {
	tools := NewCronTools(&elcron.Service{})
	if len(tools) != 3 {
		t.Fatalf("tool count = %d", len(tools))
	}
	infos := map[string]tool.Info{}
	for _, cronTool := range tools {
		infos[cronTool.Name()] = cronTool.Info()
	}
	if !infos["cron"].SuperadminOnly || infos["cron"].Hidden || infos["cron"].Risk != tool.RiskMedium {
		t.Fatalf("cron info = %#v", infos["cron"])
	}
	if deps := strings.Join(infos["cron"].DependsOn, ","); deps != "cron_query,cron_write" {
		t.Fatalf("cron dependencies = %q", deps)
	}
	info := infos["cron_query"]
	if !info.SuperadminOnly || !info.Hidden || info.Risk != tool.RiskMedium {
		t.Fatalf("cron_query info = %#v", info)
	}
	info = infos["cron_write"]
	if !info.SuperadminOnly || !info.Hidden || info.Risk != tool.RiskHigh {
		t.Fatalf("cron_write info = %#v", info)
	}
}
