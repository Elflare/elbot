package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/tool/share"
)

type cronRepositoryStub struct {
	saved           []CronSaveRequest
	info            CronInfo
	items           []CronInfo
	name            string
	includeDisabled bool
	err             error
}

func (s *cronRepositoryStub) Upsert(_ context.Context, req CronSaveRequest) (CronInfo, error) {
	s.saved = append(s.saved, req)
	return s.info, s.err
}
func (s *cronRepositoryStub) GetByName(_ context.Context, name string) (CronInfo, error) {
	s.name = name
	return s.info, s.err
}
func (s *cronRepositoryStub) List(_ context.Context, disabled bool) ([]CronInfo, error) {
	s.includeDisabled = disabled
	return s.items, s.err
}
func (s *cronRepositoryStub) DisableByName(_ context.Context, name string) error {
	s.name = name
	return s.err
}
func (s *cronRepositoryStub) DeleteByName(_ context.Context, name string) error {
	s.name = name
	return s.err
}

func cronTestTools(repo *cronRepositoryStub, validate func(string, string) error) []share.Tool {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	return NewCronTools(repo, validate, func() time.Time { return now })
}

func TestCronCreateAndPatchUseOnlyInternalRequests(t *testing.T) {
	repo := &cronRepositoryStub{}
	tools := cronTestTools(repo, nil)
	owner := contextinfo.Actor{ID: "owner", Platform: "qq", Role: contextinfo.RoleSuperadmin}
	ctx := contextinfo.WithActor(t.Context(), owner)
	_, _, err := callTool(ctx, tools[2], share.CallRequest{Arguments: json.RawMessage(`{"operation":"create","name":"reminder","schedule_mode":"once","run_after_minutes":10,"trigger_mode":"direct","message":"hello","tool_list_names":["a","a"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	req := repo.saved[0]
	if !req.ResetDelivery || req.Schedule != "10 12 25 10 *" || req.NextRunAt == nil || req.Definition.RunAt != "2026-10-25 12:10:00" || req.Definition.Actor != owner || req.Definition.SourcePlatform != "qq" || len(req.Definition.ToolListNames) != 1 {
		t.Fatalf("create = %+v", req)
	}
	repo.info = CronInfo{Name: "reminder", CreatedBy: owner, SourcePlatform: "qq", Enabled: true, ScheduleMode: "cron", CronExpr: "* * * * *", TriggerMode: "direct", Message: "hello", ToolListNames: []string{"a"}}
	// 修改者不覆盖原创建者；空字段清空，省略字段保留。
	ctx = contextinfo.WithActor(t.Context(), contextinfo.Actor{ID: "another-admin", Platform: "qq", Role: contextinfo.RoleSuperadmin})
	_, _, err = callTool(ctx, tools[2], share.CallRequest{Arguments: json.RawMessage(`{"operation":"update","name":"reminder","tool_list_names":[],"model_provider":"","model":"","enabled":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	req = repo.saved[1]
	if req.Definition.Enabled || req.NextRunAt != nil || req.ResetDelivery || len(req.Definition.ToolListNames) != 0 || req.Definition.Message != "hello" || req.Definition.Actor != owner {
		t.Fatalf("patch = %+v", req)
	}
	for _, tc := range []struct {
		args  string
		reset bool
	}{
		{`{"operation":"update","name":"reminder","title":"renamed"}`, false},
		{`{"operation":"update","name":"reminder","enabled":true}`, true},
		{`{"operation":"update","name":"reminder","message":"changed"}`, true},
	} {
		_, _, err = callTool(ctx, tools[2], share.CallRequest{Arguments: json.RawMessage(tc.args)})
		if err != nil {
			t.Fatal(err)
		}
		if req := repo.saved[len(repo.saved)-1]; req.ResetDelivery != tc.reset {
			t.Fatalf("reset = %v for %s", req.ResetDelivery, tc.args)
		}
	}
}

func TestCronValidationRejectsBeforeBridgeWrite(t *testing.T) {
	repo := &cronRepositoryStub{}
	modelErr := errors.New("unknown model")
	var provider, model string
	tools := cronTestTools(repo, func(p, m string) error { provider, model = p, m; return modelErr })
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Platform: "qq"})
	for _, args := range []string{
		`{"operation":"create","name":"bad","schedule_mode":"once","run_at":"2026-10-25 11:59:00","trigger_mode":"direct","message":"hello"}`,
		`{"operation":"create","name":"bad","schedule_mode":"cron","cron_expr":"bad expr","trigger_mode":"direct","message":"hello"}`,
		`{"operation":"create","name":"bad","schedule_mode":"cron","cron_expr":"* * * * *","trigger_mode":"llm","message":"plain text"}`,
		`{"operation":"create","name":"bad","schedule_mode":"cron","cron_expr":"* * * * *","trigger_mode":"direct","message":"hello","model_provider":"p"}`,
	} {
		if _, _, err := callTool(ctx, tools[2], share.CallRequest{Arguments: json.RawMessage(args)}); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
	_, _, err := callTool(ctx, tools[2], share.CallRequest{Arguments: json.RawMessage(`{"operation":"create","name":"bad","schedule_mode":"cron","cron_expr":"* * * * *","trigger_mode":"direct","message":"hello","model_provider":"p","model":"m"}`)})
	if !errors.Is(err, modelErr) || provider != "p" || model != "m" {
		t.Fatalf("model validation = %v, %s/%s", err, provider, model)
	}
	if len(repo.saved) != 0 {
		t.Fatal("invalid task reached bridge write")
	}
}

func TestCronQueryConsumesManagementResult(t *testing.T) {
	info := CronInfo{Name: "done", TriggerMode: "llm", Message: "task", Completed: true, Enabled: true}
	repo := &cronRepositoryStub{info: info, items: []CronInfo{info}}
	tools := cronTestTools(repo, nil)
	result, _, err := callTool(t.Context(), tools[1], share.CallRequest{Arguments: json.RawMessage(`{"name":"done"}`)})
	if err != nil || !result.NeedsElyphRules || repo.name != "done" {
		t.Fatalf("query = %+v %v", result, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result.Content), &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["completed"]) != "true" {
		t.Fatal(result.Content)
	}
	for _, key := range []string{"job", "execution", "delivery", "delivery_state", "metadata"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("internal field leaked: %s", key)
		}
	}
	result, _, err = callTool(t.Context(), tools[1], share.CallRequest{})
	if err != nil || result.Content != "[]" {
		t.Fatalf("completed filter = %+v %v", result, err)
	}
	result, _, err = callTool(t.Context(), tools[1], share.CallRequest{Arguments: json.RawMessage(`{"include_completed":true,"include_disabled":true}`)})
	if err != nil || !repo.includeDisabled || !result.NeedsElyphRules {
		t.Fatalf("include completed = %+v %v", result, err)
	}
	repo.err = errors.New("bridge unavailable")
	if _, _, err := callTool(t.Context(), tools[1], share.CallRequest{}); !errors.Is(err, repo.err) {
		t.Fatal(err)
	}
}

func TestCronScheduleRoundsCurrentMinute(t *testing.T) {
	req := CronCreateRequest{ScheduleMode: "once", RunAt: "2026-10-25 12:00:20", Enabled: true}
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.Local)
	expr, next, err := cronSchedule(&req, now)
	if err != nil || expr != "1 12 25 10 *" || req.RunAt != "2026-10-25 12:01:00" || next == nil || !next.After(now) {
		t.Fatal(req, expr, next, err)
	}
	repo := &cronRepositoryStub{info: CronInfo{Name: "reminder", SourcePlatform: "qq", Enabled: true, ScheduleMode: "once", RunAt: "2026-10-25 12:00:20", TriggerMode: "direct", Message: "hello"}}
	tools := cronTestTools(repo, nil)
	_, _, err = callTool(t.Context(), tools[2], share.CallRequest{Arguments: json.RawMessage(`{"operation":"update","name":"reminder","title":"renamed"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if saved := repo.saved[0]; !saved.ResetDelivery || saved.Definition.RunAt != "2026-10-25 12:01:00" {
		t.Fatalf("rounded update retained previous delivery: %+v", saved)
	}
}
