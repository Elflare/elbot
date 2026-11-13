package builtin

import (
	"context"
	"elbot/internal/operationresult"
	"encoding/json"
	"errors"
	"strings"
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

func (s *cronRepositoryStub) Upsert(_ context.Context, req CronSaveRequest) (operationresult.Result[CronInfo], error) {
	s.saved = append(s.saved, req)
	if s.err != nil {
		if s.err != nil {
			return operationresult.Result[CronInfo]{}, s.err
		}
		return operationresult.Result[CronInfo]{Status: operationresult.Success, Data: s.info}, nil
	}
	return operationresult.Result[CronInfo]{Status: operationresult.Success, Data: s.info}, nil
}
func (s *cronRepositoryStub) GetByName(_ context.Context, name string) (operationresult.Result[CronInfo], error) {
	s.name = name
	return operationresult.Result[CronInfo]{}, s.err
}
func (s *cronRepositoryStub) List(_ context.Context, disabled bool) (operationresult.Result[[]CronInfo], error) {
	s.includeDisabled = disabled
	if s.err != nil {
		return operationresult.Result[[]CronInfo]{}, s.err
	}
	return operationresult.Result[[]CronInfo]{Status: operationresult.Success, Data: s.items}, nil
}
func (s *cronRepositoryStub) DisableByName(_ context.Context, name string) (operationresult.Result[struct{}], error) {
	s.name = name
	return operationresult.Result[struct{}]{Status: operationresult.Success}, s.err
}
func (s *cronRepositoryStub) DeleteByName(_ context.Context, name string) (operationresult.Result[struct{}], error) {
	s.name = name
	return operationresult.Result[struct{}]{Status: operationresult.Success}, s.err
}

func cronTestTools(repo *cronRepositoryStub, validate func(string, string) operationresult.Result[struct{}]) []share.ToolDefinition {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	return NewCronTools(repo, validate, func() time.Time { return now })
}

func TestCronCreateAndPatchUseOnlyInternalRequests(t *testing.T) {
	repo := &cronRepositoryStub{}
	tools := cronTestTools(repo, nil)
	owner := contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: "qq", ID: "owner"}, Role: contextinfo.RoleSuperadmin}
	ctx := contextinfo.WithActor(t.Context(), owner)
	result, _, err := callTool(ctx, tools[2], share.CallRequest{Arguments: json.RawMessage(`{"operation":"create","name":"reminder","schedule_mode":"once","run_after_minutes":10,"trigger_mode":"direct","message":"hello","tool_list_names":["a","a"]}`)})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	req := repo.saved[0]
	if !req.ResetDelivery || req.Schedule != "10 12 25 10 *" || req.NextRunAt == nil || req.Definition.RunAt != "2026-10-25 12:10:00" || req.Definition.Actor != owner || req.Definition.SourcePlatform != "qq" || len(req.Definition.ToolListNames) != 1 {
		t.Fatalf("create = %+v", req)
	}
	repo.info = CronInfo{Name: "reminder", CreatedBy: owner, SourcePlatform: "qq", Enabled: true, ScheduleMode: "cron", CronExpr: "* * * * *", TriggerMode: "direct", Message: "hello", ToolListNames: []string{"a"}}
	// 修改者不覆盖原创建者；空字段清空，省略字段保留。
	ctx = contextinfo.WithActor(t.Context(), contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: "qq", ID: "another-admin"}, Role: contextinfo.RoleSuperadmin})
	result, _, err = callTool(ctx, tools[2], share.CallRequest{Arguments: json.RawMessage(`{"operation":"update","name":"reminder","tool_list_names":[],"model_provider":"","model":"","enabled":false}`)})
	if err != nil || result.Status != operationresult.Success {
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
		result, _, err = callTool(ctx, tools[2], share.CallRequest{Arguments: json.RawMessage(tc.args)})
		if err != nil || result.Status != operationresult.Success {
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
	tools := cronTestTools(repo, func(p, m string) operationresult.Result[struct{}] {
		provider, model = p, m
		return operationresult.Result[struct{}]{Status: operationresult.NotFound, Message: modelErr.Error()}
	})
	ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: "qq"}})
	for _, args := range []string{
		`{"operation":"create","name":"bad","schedule_mode":"once","run_at":"2026-10-25 11:59:00","trigger_mode":"direct","message":"hello"}`,
		`{"operation":"create","name":"bad","schedule_mode":"cron","cron_expr":"bad expr","trigger_mode":"direct","message":"hello"}`,
		`{"operation":"create","name":"bad","schedule_mode":"cron","cron_expr":"* * * * *","trigger_mode":"llm","message":"plain text"}`,
		`{"operation":"create","name":"bad","schedule_mode":"cron","cron_expr":"* * * * *","trigger_mode":"direct","message":"hello","model_provider":"p"}`,
	} {
		if result, _, err := callTool(ctx, tools[2], share.CallRequest{Arguments: json.RawMessage(args)}); err != nil || result.Status == operationresult.Success {
			t.Fatalf("accepted %s", args)
		}
	}
	result, _, err := callTool(ctx, tools[2], share.CallRequest{Arguments: json.RawMessage(`{"operation":"create","name":"bad","schedule_mode":"cron","cron_expr":"* * * * *","trigger_mode":"direct","message":"hello","model_provider":"p","model":"m"}`)})
	if err != nil || result.Status != operationresult.NotFound || result.Message != modelErr.Error() || provider != "p" || model != "m" {
		t.Fatalf("model validation = %v, %s/%s", err, provider, model)
	}
	if len(repo.saved) != 0 {
		t.Fatal("invalid task reached bridge write")
	}
}

func TestCronQueryConsumesManagementResult(t *testing.T) {
	info := CronInfo{Name: "done", TriggerMode: "llm", Message: "task\n第二行：\"提醒\" <内容>", Completed: true, Enabled: true}
	repo := &cronRepositoryStub{info: info, items: []CronInfo{info}}
	tools := cronTestTools(repo, nil)
	result, _, err := callTool(t.Context(), tools[1], share.CallRequest{Arguments: json.RawMessage(`{"name":"done"}`)})
	if err != nil || result.Status != operationresult.Success || !result.Data.NeedsElyphRules || repo.name != "done" {
		t.Fatalf("query = %+v %v", result, err)
	}
	for _, text := range []string{"name:done", "trigger_mode:llm", "completed:true", "enabled:true", "message:\n" + info.Message} {
		if !strings.Contains(result.Data.Content, text) {
			t.Fatalf("missing %q: %s", text, result.Data.Content)
		}
	}
	for _, key := range []string{"job", "execution", "delivery", "delivery_state", "metadata"} {
		if strings.Contains(result.Data.Content, "\n"+key+":") {
			t.Fatalf("internal field leaked: %s", key)
		}
	}
	result, _, err = callTool(t.Context(), tools[1], share.CallRequest{})
	if err != nil || result.Status != operationresult.Success || result.Data.Content != "没有符合条件的定时任务。" {
		t.Fatalf("completed filter = %+v %v", result, err)
	}
	result, _, err = callTool(t.Context(), tools[1], share.CallRequest{Arguments: json.RawMessage(`{"include_completed":true,"include_disabled":true}`)})
	if err != nil || result.Status != operationresult.Success || !repo.includeDisabled || !result.Data.NeedsElyphRules {
		t.Fatalf("include completed = %+v %v", result, err)
	}
	repo.items = append(repo.items, CronInfo{Name: "second", TriggerMode: "direct", Message: "other task", Enabled: true})
	result, _, err = callTool(t.Context(), tools[1], share.CallRequest{Arguments: json.RawMessage(`{"include_completed":true}`)})
	if err != nil || result.Status != operationresult.Success || !result.Data.NeedsElyphRules || !strings.Contains(result.Data.Content, info.Message+"\n\nname:second\n") || !strings.HasSuffix(result.Data.Content, "message:\nother task") {
		t.Fatalf("multiple tasks = %+v %v", result, err)
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
	result, _, err := callTool(t.Context(), tools[2], share.CallRequest{Arguments: json.RawMessage(`{"operation":"update","name":"reminder","title":"renamed"}`)})
	if err != nil || result.Status != operationresult.Success {
		t.Fatal(err)
	}
	if saved := repo.saved[0]; !saved.ResetDelivery || saved.Definition.RunAt != "2026-10-25 12:01:00" {
		t.Fatalf("rounded update retained previous delivery: %+v", saved)
	}
}

func TestCronWriteResultsAndErrors(t *testing.T) {
	for _, operation := range []string{"create", "update", "disable", "delete"} {
		for _, fail := range []bool{false, true} {
			t.Run(operation+map[bool]string{false: "/success", true: "/failure"}[fail], func(t *testing.T) {
				repo := &cronRepositoryStub{info: CronInfo{Name: "reminder", SourcePlatform: "qq", ScheduleMode: "cron", CronExpr: "* * * * *", TriggerMode: "direct", Message: "hello"}}
				if fail {
					repo.err = errors.New("repository unavailable")
				}
				args, err := json.Marshal(map[string]any{"operation": operation, "name": "reminder", "schedule_mode": "cron", "cron_expr": "* * * * *", "trigger_mode": "direct", "message": "hello"})
				if err != nil {
					t.Fatal(err)
				}
				ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: "qq"}})
				result, _, err := callTool(ctx, cronTestTools(repo, nil)[2], share.CallRequest{Arguments: args})
				if fail {
					if result.Data != nil || err != repo.err {
						t.Fatalf("result=%+v error=%v", result, err)
					}
					return
				}
				if err != nil || result.Data == nil {
					t.Fatalf("result=%+v error=%v", result, err)
				}
				want := map[string]string{"create": "已创建", "update": "已更新", "disable": "已停用", "delete": "已删除"}[operation] + "定时任务：reminder"
				if !strings.HasPrefix(result.Data.Content, want) {
					t.Fatalf("result=%+v want prefix=%q", result, want)
				}
				if operation == "create" || operation == "update" {
					if !strings.Contains(result.Data.Content, "\nname:reminder\n") || !strings.Contains(result.Data.Content, "cron_expr:* * * * *") || !strings.HasSuffix(result.Data.Content, "message:\nhello") || len(repo.saved) != 1 {
						t.Fatalf("result=%+v saved=%+v", result, repo.saved)
					}
				} else if result.Data.Content != want || repo.name != "reminder" {
					t.Fatalf("result=%+v target=%q", result, repo.name)
				}
			})
		}
	}
}
