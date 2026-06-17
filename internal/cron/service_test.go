package cron

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"elbot/internal/output"
	"elbot/internal/security"
	"elbot/internal/storage"
)

func TestScheduleExprOnceUsesMinutePrecision(t *testing.T) {
	expr, err := scheduleExpr(CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:05"})
	if err != nil {
		t.Fatalf("scheduleExpr: %v", err)
	}
	if expr != "4 3 2 1 *" {
		t.Fatalf("expr = %q", expr)
	}
}

func TestScheduleExprCronRequiresFiveFields(t *testing.T) {
	if _, err := scheduleExpr(CronSchedule{Mode: ScheduleCron, CronExpr: "0 1 2 3"}); err == nil {
		t.Fatal("expected invalid cron expr")
	}
	expr, err := scheduleExpr(CronSchedule{Mode: ScheduleCron, CronExpr: "0 1 * * *"})
	if err != nil {
		t.Fatalf("scheduleExpr: %v", err)
	}
	if expr != "0 1 * * *" {
		t.Fatalf("expr = %q", expr)
	}
}

func TestParseLLMResultAcceptsCodeFenceAndExtraText(t *testing.T) {
	result, err := parseLLMResult("前缀```json\n{\"completed\":true,\"need_report\":true,\"report\":\"ok\"}\n```后缀")
	if err != nil {
		t.Fatalf("parseLLMResult: %v", err)
	}
	if !result.Completed || !result.NeedReport || result.Report != "ok" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNormalizeJobNameAddsUserPrefix(t *testing.T) {
	got := normalizeJobName(" daily summary! ")
	if got != "user.cron.daily_summary" || strings.Contains(got, " ") {
		t.Fatalf("name = %q", got)
	}
}

func TestNotifyPlatformConnectedDeliversMissedDirectCronPerPlatform(t *testing.T) {
	repo := newFakeCronRepo()
	store := fakeCronStore{cron: repo}
	var sent []string
	svc := NewService(Options{
		Store:            store,
		EnabledPlatforms: []PlatformTarget{{Name: "qqonebot"}},
		SendTarget: func(ctx context.Context, target output.Target, out output.Output) error {
			sent = append(sent, target.Platform+":"+out.Text)
			return nil
		},
	})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "提醒", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "该测试啦"}, Target: CronTarget{AllEnabledPlatforms: true, SourcePlatform: "cli"}})

	svc.NotifyPlatformConnected(context.Background(), "qqonebot")
	if len(sent) != 1 || sent[0] != "qqonebot:该测试啦" {
		t.Fatalf("sent = %#v", sent)
	}
	meta := mustDecodeTestMetadata(t, repo.jobs[job.Name].Metadata)
	if !hasDeliveredPlatform(meta, "qqonebot") || hasDeliveredPlatform(meta, "cli") {
		t.Fatalf("delivered platforms = %#v", meta.Delivery.DeliveredPlatforms)
	}
	if !repo.jobs[job.Name].Enabled {
		t.Fatal("job disabled before all target platforms were delivered")
	}

	svc.NotifyPlatformConnected(context.Background(), "qqonebot")
	if len(sent) != 1 {
		t.Fatalf("duplicate send after reconnect: %#v", sent)
	}
	svc.NotifyPlatformConnected(context.Background(), "cli")
	if len(sent) != 2 || sent[1] != "cli:该测试啦" {
		t.Fatalf("sent after cli = %#v", sent)
	}
	if repo.jobs[job.Name].Enabled {
		t.Fatal("job still enabled after all target platforms were delivered")
	}
}

func TestNotifyPlatformConnectedGeneratesLLMReportForFirstConnectedTarget(t *testing.T) {
	repo := newFakeCronRepo()
	store := fakeCronStore{cron: repo}
	runner := &fakeCronRunner{text: `{"completed":true,"need_report":true,"report":"报告内容"}`}
	var sent []string
	svc := NewService(Options{
		Store:            store,
		Runner:           runner,
		EnabledPlatforms: []PlatformTarget{{Name: "qqonebot"}},
		SendTarget: func(ctx context.Context, target output.Target, out output.Output) error {
			sent = append(sent, target.Platform+":"+out.Text)
			return nil
		},
	})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "总结", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{AllEnabledPlatforms: true, SourcePlatform: "cli"}})

	svc.NotifyPlatformConnected(context.Background(), "qqonebot")
	if runner.calls != 1 {
		t.Fatalf("runner calls after qq = %d", runner.calls)
	}
	if len(sent) != 1 || sent[0] != "qqonebot:报告内容" {
		t.Fatalf("sent after qq = %#v", sent)
	}
	meta := mustDecodeTestMetadata(t, repo.jobs[job.Name].Metadata)
	if !meta.Delivery.Completed || meta.Delivery.Report != "报告内容" || !hasDeliveredPlatform(meta, "qqonebot") {
		t.Fatalf("metadata after qq = %#v", meta.Delivery)
	}

	svc.NotifyPlatformConnected(context.Background(), "cli")
	if runner.calls != 1 {
		t.Fatalf("runner should reuse cached report, calls = %d", runner.calls)
	}
	if len(sent) != 2 || sent[1] != "cli:报告内容" {
		t.Fatalf("sent after cli = %#v", sent)
	}
	svc.NotifyPlatformConnected(context.Background(), "qqonebot")
	if runner.calls != 1 || len(sent) != 2 {
		t.Fatalf("duplicate run/send: calls=%d sent=%#v", runner.calls, sent)
	}
}

func TestCreateRejectsPastAndBumpsCurrentMinuteOnceRunAt(t *testing.T) {
	svc := NewService(Options{Store: fakeCronStore{cron: newFakeCronRepo()}})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:04:30") }
	actor := security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}
	base := UpsertRequest{Name: "bad", Title: "bad", ScheduleMode: ScheduleOnce, TriggerMode: TriggerDirect, Message: "提醒", Enabled: true, Actor: actor, SourcePlatform: "cli"}

	base.RunAt = "2026-01-02 03:03:59"
	if _, err := svc.Create(context.Background(), base); err == nil || !strings.Contains(err.Error(), "当前时间：2026-01-02 03:04:30") {
		t.Fatalf("past run_at error = %v", err)
	}
	base.RunAt = "2026-01-02 03:04:59"
	job, err := svc.Create(context.Background(), base)
	if err != nil {
		t.Fatalf("current minute run_at should be bumped: %v", err)
	}
	meta := mustDecodeTestMetadata(t, job.Metadata)
	if meta.Schedule.RunAt != "2026-01-02 03:05:00" {
		t.Fatalf("bumped run_at = %q", meta.Schedule.RunAt)
	}
	base.Name = "future"
	base.RunAt = "2026-01-02 03:05:00"
	if _, err := svc.Create(context.Background(), base); err != nil {
		t.Fatalf("future run_at should be accepted: %v", err)
	}
}

func TestUpdateRejectsCurrentMinuteOnceRunAt(t *testing.T) {
	repo := newFakeCronRepo()
	svc := NewService(Options{Store: fakeCronStore{cron: repo}})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:04:30") }
	actor := security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}
	if _, err := svc.Create(context.Background(), UpsertRequest{Name: "update_bad", Title: "bad", ScheduleMode: ScheduleOnce, RunAt: "2026-01-02 03:05:00", TriggerMode: TriggerDirect, Message: "提醒", Enabled: true, Actor: actor, SourcePlatform: "cli"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	runAt := "2026-01-02 03:04:59"
	if _, err := svc.Update(context.Background(), PatchRequest{Name: "update_bad", RunAt: &runAt, Actor: actor}); err != nil {
		t.Fatalf("update current minute should be bumped: %v", err)
	}
	meta := mustDecodeTestMetadata(t, repo.jobs["user.cron.update_bad"].Metadata)
	if meta.Schedule.RunAt != "2026-01-02 03:05:00" {
		t.Fatalf("updated run_at = %q", meta.Schedule.RunAt)
	}
}

func TestCreateLLMCronRequiresElyphTask(t *testing.T) {
	svc := NewService(Options{Store: fakeCronStore{cron: newFakeCronRepo()}})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:04:30") }
	actor := security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}
	base := UpsertRequest{Name: "daily", Title: "daily", ScheduleMode: ScheduleOnce, RunAt: "2026-01-02 03:05:00", TriggerMode: TriggerLLM, Message: "自然语言任务", Enabled: true, Actor: actor, SourcePlatform: "cli"}
	if _, err := svc.Create(context.Background(), base); err == nil || !strings.Contains(err.Error(), "#task") {
		t.Fatalf("invalid LLM cron error = %v", err)
	}
	base.Message = testElyphTask("daily_task")
	if _, err := svc.Create(context.Background(), base); err != nil {
		t.Fatalf("valid ELyph LLM cron with different task name should be accepted: %v", err)
	}

}

func TestCreateAndUpdateLLMCronToolListNames(t *testing.T) {
	repo := newFakeCronRepo()
	svc := NewService(Options{Store: fakeCronStore{cron: repo}})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:04:30") }
	actor := security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}
	job, err := svc.Create(context.Background(), UpsertRequest{Name: "tools", Title: "tools", ScheduleMode: ScheduleOnce, RunAt: "2026-01-02 03:05:00", TriggerMode: TriggerLLM, Message: testElyphTask("tools"), ToolListNames: []string{" web_search ", "web_search", "shell"}, Enabled: true, Actor: actor, SourcePlatform: "cli"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	meta := mustDecodeTestMetadata(t, job.Metadata)
	if got := strings.Join(meta.LLM.ToolListNames, ","); got != "web_search,shell" {
		t.Fatalf("created tool_list_names = %q", got)
	}
	title := "renamed"
	if _, err := svc.Update(context.Background(), PatchRequest{Name: "tools", Title: &title, Actor: actor}); err != nil {
		t.Fatalf("update title: %v", err)
	}
	meta = mustDecodeTestMetadata(t, repo.jobs["user.cron.tools"].Metadata)
	if got := strings.Join(meta.LLM.ToolListNames, ","); got != "web_search,shell" {
		t.Fatalf("tool_list_names should be preserved = %q", got)
	}
	empty := []string{}
	if _, err := svc.Update(context.Background(), PatchRequest{Name: "tools", ToolListNames: &empty, Actor: actor}); err != nil {
		t.Fatalf("clear tool names: %v", err)
	}
	meta = mustDecodeTestMetadata(t, repo.jobs["user.cron.tools"].Metadata)
	if len(meta.LLM.ToolListNames) != 0 {
		t.Fatalf("tool_list_names should be cleared = %#v", meta.LLM.ToolListNames)
	}
}

func TestRunLLMReportPassesToolListNames(t *testing.T) {
	repo := newFakeCronRepo()
	runner := &fakeCronRunner{text: `{"completed":true,"need_report":false,"report":"ok"}`}
	svc := NewService(Options{Store: fakeCronStore{cron: repo}, Runner: runner})
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "LLM", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}, LLM: CronLLMMetadata{ToolListNames: []string{"web_search"}}})
	meta := mustDecodeTestMetadata(t, job.Metadata)
	if _, _, err := svc.runLLMReport(context.Background(), *job, meta); err != nil {
		t.Fatalf("runLLMReport: %v", err)
	}
	if len(runner.requests) != 1 || strings.Join(runner.requests[0].ToolListNames, ",") != "web_search" {
		t.Fatalf("runner request = %#v", runner.requests)
	}
}

func TestCreateDirectCronDoesNotRequireElyphTask(t *testing.T) {
	svc := NewService(Options{Store: fakeCronStore{cron: newFakeCronRepo()}})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:04:30") }
	actor := security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}
	req := UpsertRequest{Name: "direct", Title: "direct", ScheduleMode: ScheduleOnce, RunAt: "2026-01-02 03:05:00", TriggerMode: TriggerDirect, Message: "自然语言提醒", Enabled: true, Actor: actor, SourcePlatform: "cli"}
	if _, err := svc.Create(context.Background(), req); err != nil {
		t.Fatalf("direct cron should accept plain text: %v", err)
	}
}

func TestRunLLMReportSendsNonEmptyReportEvenWhenNeedReportFalse(t *testing.T) {
	repo := newFakeCronRepo()
	runner := &fakeCronRunner{text: `{"completed":true,"need_report":false,"report":"仍然汇报"}`}
	svc := NewService(Options{Store: fakeCronStore{cron: repo}, Runner: runner})
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "LLM", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}})

	meta := mustDecodeTestMetadata(t, job.Metadata)
	updated, report, err := svc.runLLMReport(context.Background(), *job, meta)
	if err != nil {
		t.Fatalf("runLLMReport: %v", err)
	}
	if !strings.Contains(runner.requests[0].Prompt, "ELyph v0.2规则") || !strings.Contains(runner.requests[0].Prompt, "#task test") || !strings.Contains(runner.requests[0].Prompt, "最终回复必须是严格 JSON") {

		t.Fatalf("cron prompt missing ELyph rules/task/json protocol: %q", runner.requests[0].Prompt)
	}
	if report != "仍然汇报" || updated.Delivery.Report != "仍然汇报" {
		t.Fatalf("report=%q delivery=%q", report, updated.Delivery.Report)
	}
}

func TestRunLLMReportRetriesInvalidJSONOnce(t *testing.T) {
	repo := newFakeCronRepo()
	runner := &fakeCronRunner{texts: []string{"不是 JSON", `{"completed":true,"need_report":true,"report":"修正后报告"}`}}
	svc := NewService(Options{Store: fakeCronStore{cron: repo}, Runner: runner})
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "LLM", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}})

	meta := mustDecodeTestMetadata(t, job.Metadata)
	updated, report, err := svc.runLLMReport(context.Background(), *job, meta)
	if err != nil {
		t.Fatalf("runLLMReport: %v", err)
	}
	if runner.calls != 2 {
		t.Fatalf("runner calls = %d", runner.calls)
	}
	if runner.requests[1].SessionID != "cron-session" {
		t.Fatalf("retry session id = %q", runner.requests[1].SessionID)
	}
	if !strings.Contains(runner.requests[1].Prompt, "你返回的格式有误") {
		t.Fatalf("retry prompt = %q", runner.requests[1].Prompt)
	}
	if report != "修正后报告" || updated.Delivery.Report != "修正后报告" {
		t.Fatalf("report=%q delivery=%q", report, updated.Delivery.Report)
	}
}

func TestRunLLMSendsAdminNoticeWhenRetryStillInvalid(t *testing.T) {
	repo := newFakeCronRepo()
	runner := &fakeCronRunner{texts: []string{"不是 JSON", "仍然不是 JSON"}}
	var sent []string
	svc := NewService(Options{
		Store:  fakeCronStore{cron: repo},
		Runner: runner,
		SendTarget: func(ctx context.Context, target output.Target, out output.Output) error {
			sent = append(sent, target.Platform+":"+out.Text)
			return nil
		},
	})
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "LLM", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}})

	meta := mustDecodeTestMetadata(t, job.Metadata)
	if err := svc.runLLM(context.Background(), *job, meta); err == nil {
		t.Fatal("expected parse error")
	}
	if runner.calls != 2 {
		t.Fatalf("runner calls = %d", runner.calls)
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "cli:cron 任务 LLM 解析格式失败") || !strings.Contains(sent[0], "请 /resume 到 cron session 查看详情") {
		t.Fatalf("sent = %#v", sent)
	}
}

func TestNotifyPlatformConnectedSkipsAlreadyDeliveredPlatform(t *testing.T) {
	repo := newFakeCronRepo()
	store := fakeCronStore{cron: repo}
	var sent []string
	svc := NewService(Options{
		Store: store,
		SendTarget: func(ctx context.Context, target output.Target, out output.Output) error {
			sent = append(sent, target.Platform+":"+out.Text)
			return nil
		},
	})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "提醒", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "该测试啦"}, Target: CronTarget{SourcePlatform: "cli"}, Delivery: CronDeliveryMetadata{Completed: true, Report: "该测试啦", DeliveredPlatforms: []string{"cli"}}})

	svc.NotifyPlatformConnected(context.Background(), "cli")
	if len(sent) != 0 {
		t.Fatalf("already delivered platform was sent again: %#v", sent)
	}
}

func TestListHidesCompletedCronByDefault(t *testing.T) {
	repo := newFakeCronRepo()
	svc := NewService(Options{Store: fakeCronStore{cron: repo}})
	actor := security.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: security.RoleSuperadmin}
	upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "done", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "done"}, Target: CronTarget{SourcePlatform: "cli"}, Delivery: CronDeliveryMetadata{Completed: true, DeliveredPlatforms: []string{"cli"}}})

	views, err := svc.List(context.Background(), true, false, actor)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != 0 {
		t.Fatalf("completed cron should be hidden: %#v", views)
	}
	views, err = svc.List(context.Background(), true, true, actor)
	if err != nil {
		t.Fatalf("List include completed: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("completed cron should be shown: %#v", views)
	}
}

func TestCronSendAuditIncludesPlatform(t *testing.T) {
	var events []string
	svc := NewService(Options{
		Store: fakeCronStore{cron: newFakeCronRepo()},
		Audit: func(event string, attrs ...any) {
			events = append(events, event+":"+attrsString(attrs, "platform"))
		},
		SendTarget: func(ctx context.Context, target output.Target, out output.Output) error { return nil },
	})
	if err := svc.sendToPlatforms(context.Background(), "user.cron.audit", []string{"cli"}, "hello"); err != nil {
		t.Fatalf("sendToPlatforms: %v", err)
	}
	joined := strings.Join(events, "|")
	if !strings.Contains(joined, "cron.send_started:cli") || !strings.Contains(joined, "cron.send_completed:cli") {
		t.Fatalf("events = %#v", events)
	}
}

type fakeCronStore struct{ cron storage.CronJobRepository }

func (s fakeCronStore) Sessions() storage.SessionRepository                { return nil }
func (s fakeCronStore) Messages() storage.MessageRepository                { return nil }
func (s fakeCronStore) ContextSummaries() storage.ContextSummaryRepository { return nil }
func (s fakeCronStore) ToolCalls() storage.ToolCallRepository              { return nil }
func (s fakeCronStore) CronJobs() storage.CronJobRepository                { return s.cron }
func (s fakeCronStore) ElnisEvents() storage.ElnisEventRepository          { return nil }
func (s fakeCronStore) Close() error                                       { return nil }

type fakeCronRunner struct {
	text     string
	texts    []string
	calls    int
	requests []RunCronMessageRequest
}

func (r *fakeCronRunner) RunCronMessage(ctx context.Context, req RunCronMessageRequest) (RunCronMessageResult, error) {
	r.requests = append(r.requests, req)
	text := r.text
	if r.calls < len(r.texts) {
		text = r.texts[r.calls]
	}
	r.calls++
	return RunCronMessageResult{SessionID: "cron-session", Text: text}, nil
}

func testElyphTask(name string) string {
	return "#task " + name + " - test task\n-> $report:str\n"
}

func upsertTestCronJob(t *testing.T, repo *fakeCronRepo, meta Metadata) *storage.CronJob {
	t.Helper()
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	job, err := repo.Upsert(context.Background(), storage.UpsertCronJobRequest{Name: "user.cron.test", Handler: UserHandlerName, Schedule: "4 3 2 1 *", Enabled: true, Metadata: string(data)})
	if err != nil {
		t.Fatalf("upsert job: %v", err)
	}
	return job
}

func mustDecodeTestMetadata(t *testing.T, raw string) Metadata {
	t.Helper()
	meta, err := decodeMetadata(raw)
	if err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	return meta
}

func mustParseTestTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := parseRunAt(value)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return parsed
}

func attrsString(attrs []any, key string) string {
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i] == key {
			if value, ok := attrs[i+1].(string); ok {
				return value
			}
		}
	}
	return ""
}
