package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/tool/runtimeinfo"
	"elbot/internal/tool/share"
)

type cronServiceStub struct {
	share.CronService
	create              share.CronCreateRequest
	update              share.CronUpdateRequest
	actor               contextinfo.Actor
	name                string
	disabled, completed bool
	job                 share.CronInfo
	err                 error
}

func (s *cronServiceStub) CreateCron(_ context.Context, req share.CronCreateRequest) (share.CronInfo, error) {
	s.create = req
	return s.job, s.err
}
func (s *cronServiceStub) UpdateCron(_ context.Context, req share.CronUpdateRequest) (share.CronInfo, error) {
	s.update = req
	return s.job, s.err
}
func (s *cronServiceStub) GetCron(_ context.Context, name string, actor contextinfo.Actor) (share.CronInfo, error) {
	s.name, s.actor = name, actor
	return s.job, s.err
}
func (s *cronServiceStub) ListCrons(_ context.Context, disabled, completed bool, actor contextinfo.Actor) ([]share.CronInfo, error) {
	s.disabled, s.completed, s.actor = disabled, completed, actor
	return []share.CronInfo{s.job}, s.err
}

func TestCronWriteServiceRequests(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	s := &cronServiceStub{job: share.CronInfo{Name: "reminder"}}
	tool := NewCronTools(s, runtimeinfo.Info{Now: func() time.Time { return now }})[2]
	actor := contextinfo.Actor{ID: "owner", Platform: "qq", Role: contextinfo.RoleSuperadmin}
	ctx := contextinfo.WithActor(t.Context(), actor)
	result, _, err := tool.Call(ctx, share.CallRequest{Arguments: json.RawMessage(`{"operation":"create","name":"reminder","schedule_mode":"once","run_after_minutes":10,"trigger_mode":"direct","message":"hello"}`)})
	if err != nil || result == nil {
		t.Fatalf("create = %v, %v", result, err)
	}
	if s.create.Name != "reminder" || !s.create.Enabled || s.create.Actor != actor || s.create.SourcePlatform != "qq" || s.create.RunAt != "2026-10-25 12:10:00" || s.create.Message != "hello" {
		t.Fatalf("create request = %+v", s.create)
	}
	_, _, err = tool.Call(ctx, share.CallRequest{Arguments: json.RawMessage(`{"operation":"update","name":"reminder","tool_list_names":[],"model_provider":"","model":"","enabled":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	p := s.update
	if p.Actor != actor || p.ToolListNames == nil || len(*p.ToolListNames) != 0 || p.ModelProvider == nil || *p.ModelProvider != "" || p.Model == nil || *p.Model != "" || p.Enabled == nil || *p.Enabled || p.Message != nil || p.RunAt != nil {
		t.Fatalf("clear patch = %+v", p)
	}
	_, _, err = tool.Call(ctx, share.CallRequest{Arguments: json.RawMessage(`{"operation":"update","name":"reminder"}`)})
	if err != nil || s.update.ToolListNames != nil || s.update.Model != nil || s.update.Enabled != nil {
		t.Fatalf("omitted patch = %+v, %v", s.update, err)
	}
}

func TestCronQueryReturnsManagementInfo(t *testing.T) {
	s := &cronServiceStub{job: share.CronInfo{Name: "reminder", TriggerMode: "llm", Message: "task", Enabled: true}}
	tool := NewCronTools(s)[1]
	actor := contextinfo.Actor{ID: "owner", Platform: "qq"}
	ctx := contextinfo.WithActor(t.Context(), actor)
	result, _, err := tool.Call(ctx, share.CallRequest{Arguments: json.RawMessage(`{"name":"reminder"}`)})
	if err != nil || result == nil || !result.NeedsElyphRules || s.actor != actor || s.name != "reminder" {
		t.Fatalf("query = %+v, %v", result, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result.Content), &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["trigger_mode"]) != `"llm"` || string(fields["message"]) != `"task"` {
		t.Fatalf("info = %s", result.Content)
	}
	for _, key := range []string{"job", "execution", "delivery"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("internal %q leaked: %s", key, result.Content)
		}
	}
	result, _, err = tool.Call(ctx, share.CallRequest{Arguments: json.RawMessage(`{"include_disabled":true,"include_completed":true}`)})
	if err != nil || result == nil || !result.NeedsElyphRules || !s.disabled || !s.completed || s.actor != actor {
		t.Fatalf("list = %+v, %v", result, err)
	}
	s.err = errors.New("backend failed")
	if _, _, err := tool.Call(ctx, share.CallRequest{}); !errors.Is(err, s.err) {
		t.Fatalf("error = %v", err)
	}
}
