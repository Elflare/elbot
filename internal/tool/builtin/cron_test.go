package builtin

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCronToolCallReturnsCurrentTimeHint(t *testing.T) {
	result, _, err := callTool(context.Background(), NewCronTools(nil, nil, time.Now)[0], toolshare.CallRequest{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(result.Content, "当前本地时间") {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestResolveRunAtUsesRelativeOffsets(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	got, err := resolveRunAtAt("", relativeOffset{Minutes: 10}, now)
	if err != nil {
		t.Fatalf("resolveRunAt: %v", err)
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", got, time.Local)
	if err != nil {
		t.Fatalf("parse resolved run_at: %v", err)
	}
	if d := parsed.Sub(now); d != 10*time.Minute {
		t.Fatalf("resolved duration = %s, run_at=%s", d, got)
	}
}

func TestResolveRunAtUsesWeeksAndMonths(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	got, err := resolveRunAtAt("", relativeOffset{Weeks: 1, Months: 1}, now)
	if err != nil {
		t.Fatalf("resolveRunAt: %v", err)
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", got, time.Local)
	if err != nil {
		t.Fatalf("parse resolved run_at: %v", err)
	}
	if want := time.Date(2026, 2, 9, 3, 4, 5, 0, time.Local); !parsed.Equal(want) {
		t.Fatalf("resolved run_at = %s, want %s", got, want)
	}
}

func TestResolveRunAtRejectsAbsoluteAndRelativeTogether(t *testing.T) {
	_, err := resolveRunAtAt("2026-01-02 03:04:05", relativeOffset{Minutes: 1}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local))
	if err == nil || !strings.Contains(err.Error(), "不能和 run_after_*") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveUpdateRunAtReturnsPointerForRelativeOffset(t *testing.T) {
	got, err := resolveUpdateRunAtAt(nil, relativeOffset{Hours: 1}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local))
	if err != nil {
		t.Fatalf("resolveUpdateRunAt: %v", err)
	}
	if got == nil || *got != "2026-01-02 04:04:05" {
		t.Fatalf("run_at pointer = %#v", got)
	}
}

func TestCronWritePreparationUsesArgumentDetails(t *testing.T) {
	for _, args := range []string{
		`{"operation":"create","name":"drink","schedule_mode":"once","run_after_minutes":10,"trigger_mode":"direct","message":"喝水\n起来活动一下"}`,
		`{"operation":"update","name":"drink","tool_list_names":[],"model_provider":"","model":"","enabled":false}`,
		`{"operation":"disable","name":"drink"}`,
		`{"operation":"delete","name":"drink"}`,
	} {
		t.Run(args, func(t *testing.T) {
			repo := &cronRepositoryStub{}
			tool := cronTestTools(repo, nil)[2]
			req := toolshare.CallRequest{ID: "call", Name: tool.Definition.Name, Arguments: json.RawMessage(args)}
			prepared, err := tool.PrepareCall(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if got := prepared.Preview(); got != "" {
				t.Errorf("custom detail hides arguments: %q", got)
			}
			if !prepared.Matches(req) || string(req.Arguments) != args {
				t.Error("preparation changed call arguments")
			}
			if len(repo.saved) != 0 || repo.name != "" {
				t.Fatal("preparation accessed the repository")
			}
		})
	}
}

func TestCronToolsRiskAndVisibility(t *testing.T) {
	tools := NewCronTools(nil, nil, time.Now)
	if len(tools) != 3 {
		t.Fatalf("tool count = %d", len(tools))
	}
	infos := map[string]toolshare.Info{}
	for _, cronTool := range tools {
		infos[cronTool.Definition.Name] = cronTool.Definition
	}
	if !infos["cron"].SuperadminOnly || infos["cron"].Hidden || infos["cron"].Risk != toolshare.RiskMedium {
		t.Fatalf("cron info = %#v", infos["cron"])
	}
	if deps := strings.Join(infos["cron"].DependsOn, ","); deps != "cron_query,cron_write" {
		t.Fatalf("cron dependencies = %q", deps)
	}
	info := infos["cron_query"]
	if !info.SuperadminOnly || !info.Hidden || info.Risk != toolshare.RiskMedium {
		t.Fatalf("cron_query info = %#v", info)
	}
	info = infos["cron_write"]
	if !info.SuperadminOnly || !info.Hidden || info.Risk != toolshare.RiskHigh {
		t.Fatalf("cron_write info = %#v", info)
	}
}
