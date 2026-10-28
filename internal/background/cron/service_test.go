package cron

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/agent"
	background "elbot/internal/background/share"
	"elbot/internal/config"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/testutil"
)

func TestParseLLMResultAcceptsCodeFenceAndExtraText(t *testing.T) {
	result, err := parseLLMResult("前缀```json\n{\"completed\":true,\"need_report\":true,\"report\":\"ok\"}\n```后缀")
	if err != nil {
		t.Fatalf("parseLLMResult: %v", err)
	}
	if !result.Completed || !result.NeedReport || result.Report != "ok" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPlatformConnectedDeliversMissedDirectCronPerPlatform(t *testing.T) {
	repo := newFakeCronRepo()
	store := repo
	var sent []string
	svc := newTestCronService(t, Options{
		Store:            store,
		EnabledPlatforms: []PlatformTarget{{Name: "qqonebot"}},
		SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
			sent = append(sent, target.Platform+":"+outputs[0].Text)
			return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
		},
	})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "提醒", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "该测试啦"}, Target: CronTarget{AllEnabledPlatforms: true, SourcePlatform: "cli"}})

	startCronPlatformEvents(t, svc)

	emitCronPlatformAndWait(t, svc, "qqonebot")
	if len(sent) != 1 || sent[0] != "qqonebot:提醒补发：\n\n该测试啦" {
		t.Fatalf("sent = %#v", sent)
	}
	state := mustDecodeTestDelivery(t, repo.jobs[job.Name].DeliveryState)
	if !deliveryPlatformDone(state, "qqonebot") || deliveryPlatformDone(state, "cli") {
		t.Fatalf("delivery state = %#v", state)
	}
	if !repo.jobs[job.Name].Enabled {
		t.Fatal("job disabled before all target platforms were delivered")
	}

	emitCronPlatformAndWait(t, svc, "qqonebot")
	if len(sent) != 1 {
		t.Fatalf("duplicate send after reconnect: %#v", sent)
	}
	emitCronPlatformAndWait(t, svc, "cli")
	if len(sent) != 2 || sent[1] != "cli:提醒补发：\n\n该测试啦" {
		t.Fatalf("sent after cli = %#v", sent)
	}
	if repo.jobs[job.Name].Enabled {
		t.Fatal("job still enabled after all target platforms were delivered")
	}
}

func TestPlatformConnectedGeneratesLLMReportForFirstConnectedTarget(t *testing.T) {
	repo := newFakeCronRepo()
	store := repo
	runner := &fakeCronRunner{text: `{"completed":true,"need_report":true,"report":"报告内容"}`}
	var sent []string
	svc := newTestCronService(t, Options{
		Store:            store,
		Runner:           runner,
		EnabledPlatforms: []PlatformTarget{{Name: "qqonebot"}},
		SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
			sent = append(sent, target.Platform+":"+outputs[0].Text)
			return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
		},
	})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "总结", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{AllEnabledPlatforms: true, SourcePlatform: "cli"}})

	startCronPlatformEvents(t, svc)

	emitCronPlatformAndWait(t, svc, "qqonebot")
	if runner.calls != 1 {
		t.Fatalf("runner calls after qq = %d", runner.calls)
	}
	if len(sent) != 1 || sent[0] != "qqonebot:总结补发：\n\n报告内容" {
		t.Fatalf("sent after qq = %#v", sent)
	}
	state := mustDecodeTestDelivery(t, repo.jobs[job.Name].DeliveryState)
	if !state.ReportReady || state.Report != "报告内容" || !deliveryPlatformDone(state, "qqonebot") {
		t.Fatalf("delivery after qq = %#v", state)
	}

	emitCronPlatformAndWait(t, svc, "cli")
	if runner.calls != 1 {
		t.Fatalf("runner should reuse cached report, calls = %d", runner.calls)
	}
	if len(sent) != 2 || sent[1] != "cli:总结补发：\n\n报告内容" {
		t.Fatalf("sent after cli = %#v", sent)
	}
	emitCronPlatformAndWait(t, svc, "qqonebot")
	if runner.calls != 1 || len(sent) != 2 {
		t.Fatalf("duplicate run/send: calls=%d sent=%#v", runner.calls, sent)
	}
}

func TestMissedOnceReportTextFormatsTitleAndBody(t *testing.T) {
	if got := missedOnceReportText("日报", " 报告内容 "); got != "日报补发：\n\n报告内容" {
		t.Fatalf("text = %q", got)
	}
	if got := missedOnceReportText("日报", ""); got != "日报补发：" {
		t.Fatalf("empty report text = %q", got)
	}
}

func TestRunLLMReportPassesToolListNames(t *testing.T) {
	repo := newFakeCronRepo()
	runner := &fakeCronRunner{text: `{"completed":true,"need_report":false,"report":"ok"}`}
	svc := newTestCronService(t, Options{Store: repo, Runner: runner})
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "LLM", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}, LLM: CronLLMMetadata{ToolListNames: []string{"web_search"}}})
	meta := mustDecodeTestMetadata(t, job.Metadata)
	if _, _, _, err := svc.runLLMReport(context.Background(), *job, meta, CronDeliveryState{RunID: "run"}); err != nil {
		t.Fatalf("runLLMReport: %v", err)
	}
	if len(runner.requests) != 1 || strings.Join(runner.requests[0].ToolListNames, ",") != "web_search" {
		t.Fatalf("runner request = %#v", runner.requests)
	}
	if runner.requests[0].SandboxSubdir != "cron/test" {
		t.Fatalf("sandbox subdir = %q", runner.requests[0].SandboxSubdir)
	}
}

func TestRunLLMMapsReportNoticeToBackgroundMessage(t *testing.T) {
	ctx := context.Background()
	store := newFakeCronRepo()
	runner := &fakeCronRunner{text: `{"completed":true,"need_report":true,"report":"报告内容"}`}
	svc := newTestCronService(t, Options{
		Store:            store,
		Runner:           runner,
		EnabledPlatforms: []PlatformTarget{{Name: "qq-onebot", SuperadminIDs: []string{"1001"}}},
		SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
			if target.Platform == "qq-onebot" && target.PrivateUserID == "1001" {
				return delivery.Receipt{PlatformMessageIDs: []string{"notice-1"}, SentMessages: []delivery.SentMessage{{Platform: "qq-onebot", ScopeID: "private:1001", PlatformMessageID: "notice-1"}}}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
			}
			return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
		},
	})
	job := upsertTestCronJob(t, store, Metadata{Kind: metadataKind, Version: 1, Title: "总结", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{AllEnabledPlatforms: true, SourcePlatform: "cli"}})
	meta := mustDecodeTestMetadata(t, job.Metadata)
	if _, err := svc.runLLM(ctx, *job, meta); err != nil {
		t.Fatalf("runLLM: %v", err)
	}
	if len(store.mappings) != 1 || store.mappings[0] != (background.ReportMessageMap{Platform: "qq-onebot", PlatformScopeID: "private:1001", PlatformMessageID: "notice-1", SessionID: "cron-session", MessageID: "cron-message"}) {
		t.Fatalf("mappings = %#v", store.mappings)
	}
}

func TestCopyCronSessionUsesTaskSourceAndPreservesMetadata(t *testing.T) {
	var copies []agent.BackgroundCopyRequest
	svc := newTestCronService(t, Options{CopyBackground: func(_ context.Context, req agent.BackgroundCopyRequest) (string, error) {
		copies = append(copies, req)
		return "copy", nil
	}, EnabledPlatforms: []PlatformTarget{{Name: "qq-onebot", SuperadminIDs: []string{"1"}}, {Name: "telegram", SuperadminIDs: []string{"2"}}, {Name: "cli"}}})
	meta := Metadata{Title: "cron", Target: CronTarget{AllEnabledPlatforms: true, SourcePlatform: "cli"}}
	if err := svc.copySessionToBroadcastTargets(context.Background(), "source", meta, "user.cron.test"); err != nil {
		t.Fatal(err)
	}
	if len(copies) != 2 {
		t.Fatalf("copies=%#v", copies)
	}
	for _, req := range copies {
		if req.SourceSessionID != "source" || req.Scope.Platform == "cli" || req.Scope.PlatformScopeID != "cron:user.cron.test" || string(req.Metadata["cron_broadcast_copy"]) != "true" || string(req.Metadata["cron_source_session_id"]) != `"source"` || string(req.Metadata["cron_job_name"]) != `"user.cron.test"` {
			t.Fatalf("copy=%#v", req)
		}
	}
}

func TestRunLLMSendsReportSegments(t *testing.T) {
	repo := newFakeCronRepo()
	root := filepath.Join(t.TempDir(), "sandbox")
	if err := os.MkdirAll(filepath.Join(root, "cron", "test"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cron", "test", "chart.png"), []byte("png"), 0644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeCronRunner{text: `{"completed":true,"need_report":true,"report":"见图","report_segments":[{"type":"image","url":"chart.png"}]}`}
	sent := []delivery.Kind{}
	svc := newTestCronService(t, Options{Store: repo, Runner: runner, SandboxRoot: root, SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
		sent = append(sent, outputs[0].Kind)
		if outputs[0].Kind == delivery.KindImage && outputs[0].Source.Path != filepath.Join(root, "cron", "test", "chart.png") {
			t.Fatalf("image path = %q", outputs[0].Source.Path)
		}
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}})
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "LLM", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}})
	meta := mustDecodeTestMetadata(t, job.Metadata)
	if _, err := svc.runLLM(context.Background(), *job, meta); err != nil {
		t.Fatalf("runLLM: %v", err)
	}
	if len(sent) != 2 || sent[0] != delivery.KindText || sent[1] != delivery.KindImage {
		t.Fatalf("sent kinds = %#v", sent)
	}
	updated := mustDecodeTestDelivery(t, repo.jobs["user.cron.test"].DeliveryState)
	if len(updated.ReportSegments) != 1 || updated.ReportSegments[0].Source.Path != "chart.png" {
		t.Fatalf("persisted report segments = %#v", updated.ReportSegments)
	}
}

func TestMissedOnceFallsBackToTextWhenSandboxAttachmentIsMissing(t *testing.T) {
	repo := newFakeCronRepo()
	var sent []delivery.Output
	svc := newTestCronService(t, Options{Store: repo, SandboxRoot: t.TempDir(), SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
		sent = append(sent, outputs[0])
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 2, Title: "日报", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}})
	state := CronDeliveryState{RunID: "run", ReportReady: true, TaskCompleted: true, Report: "正文", ReportSegments: []delivery.Output{{Kind: delivery.KindImage, Source: delivery.Source{Path: "missing.png"}}}, ReportSessionID: "session"}
	repo.jobs[job.Name].DeliveryState = mustMarshalTestDelivery(t, state)
	repo.jobs[job.Name].DeliveryToken = "session"

	svc.handlePlatformConnected(context.Background(), "cli")
	if len(sent) != 2 || sent[0].Text != "日报补发：\n\n正文" || sent[1].Text != "路径 missing.png 附件发送失败" {
		t.Fatalf("sent = %#v", sent)
	}
	if repo.jobs[job.Name].Enabled {
		t.Fatal("job should be disabled after fallback text is delivered")
	}
	got := mustDecodeTestDelivery(t, repo.jobs[job.Name].DeliveryState)
	if status := findDeliveryOutputStatus(*findDeliveryTargetState(got, "cli|superadmins"), "segment:0"); status != DeliveryFallbackDelivered {
		t.Fatalf("segment status = %q", status)
	}
}

func TestMissedOnceFallsBackToURLTextAfterRemoteMediaSendFails(t *testing.T) {
	repo := newFakeCronRepo()
	var sent []delivery.Output
	svc := newTestCronService(t, Options{Store: repo, SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
		sent = append(sent, outputs[0])
		if outputs[0].Kind == delivery.KindImage {
			{
				err := errors.New("media rejected")
				logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
				if err != nil {
					logResult.ResultStatus = globalevents.ResultFailed
					logResult.Content = err.Error()
					if err == context.Canceled {
						logResult.ResultStatus = globalevents.ResultCanceled
					}
				}
				return delivery.Receipt{}, logResult, err
			}
		}
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 2, Title: "日报", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}})
	state := CronDeliveryState{RunID: "run", ReportReady: true, TaskCompleted: true, Report: "正文", ReportSegments: []delivery.Output{{Kind: delivery.KindImage, Source: delivery.Source{URL: "https://example.com/chart.png"}}}, ReportSessionID: "session"}
	repo.jobs[job.Name].DeliveryState = mustMarshalTestDelivery(t, state)
	repo.jobs[job.Name].DeliveryToken = "session"

	svc.handlePlatformConnected(context.Background(), "cli")
	if len(sent) != 3 || sent[1].Source.URL != "https://example.com/chart.png" || sent[2].Text != "url https://example.com/chart.png 发送失败" {
		t.Fatalf("sent = %#v", sent)
	}
	if repo.jobs[job.Name].Enabled {
		t.Fatal("job should be disabled after URL fallback text is delivered")
	}
}

func TestMissedOnceReconnectRetriesOnlyPendingFallbackText(t *testing.T) {
	repo := newFakeCronRepo()
	reportSends := 0
	mediaSends := 0
	fallbackSends := 0
	const fallback = "url https://example.com/chart.png 发送失败"
	svc := newTestCronService(t, Options{Store: repo, SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
		switch {
		case outputs[0].Kind == delivery.KindImage:
			mediaSends++
			{
				err := errors.New("media rejected")
				logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
				if err != nil {
					logResult.ResultStatus = globalevents.ResultFailed
					logResult.Content = err.Error()
					if err == context.Canceled {
						logResult.ResultStatus = globalevents.ResultCanceled
					}
				}
				return delivery.Receipt{}, logResult, err
			}
		case outputs[0].Text == fallback:
			fallbackSends++
			if fallbackSends == 1 {
				{
					err := errors.New("fallback rejected")
					logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
					if err != nil {
						logResult.ResultStatus = globalevents.ResultFailed
						logResult.Content = err.Error()
						if err == context.Canceled {
							logResult.ResultStatus = globalevents.ResultCanceled
						}
					}
					return delivery.Receipt{}, logResult, err
				}
			}
		case outputs[0].Text == "日报补发：\n\n正文":
			reportSends++
		}
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 2, Title: "日报", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}})
	state := CronDeliveryState{RunID: "run", ReportReady: true, TaskCompleted: true, Report: "正文", ReportSegments: []delivery.Output{{Kind: delivery.KindImage, Source: delivery.Source{URL: "https://example.com/chart.png"}}}, ReportSessionID: "session"}
	repo.jobs[job.Name].DeliveryState = mustMarshalTestDelivery(t, state)
	repo.jobs[job.Name].DeliveryToken = "session"

	svc.handlePlatformConnected(context.Background(), "cli")
	svc.handlePlatformConnected(context.Background(), "cli")
	if reportSends != 1 || mediaSends != 1 || fallbackSends != 2 {
		t.Fatalf("report=%d media=%d fallback=%d", reportSends, mediaSends, fallbackSends)
	}
	if repo.jobs[job.Name].Enabled {
		t.Fatal("job should be disabled after reconnect delivers fallback text")
	}
}

func TestMissedOnceReusesReportWhenTaskCompletedIsFalse(t *testing.T) {
	repo := newFakeCronRepo()
	runner := &fakeCronRunner{text: `{"completed":false,"need_report":true,"report":"任务被阻塞"}`}
	svc := newTestCronService(t, Options{Store: repo, Runner: runner, EnabledPlatforms: []PlatformTarget{{Name: "qqonebot"}}, SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 2, Title: "阻塞任务", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{AllEnabledPlatforms: true, SourcePlatform: "cli"}})

	svc.handlePlatformConnected(context.Background(), "qqonebot")
	svc.handlePlatformConnected(context.Background(), "cli")
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d", runner.calls)
	}
	state := mustDecodeTestDelivery(t, repo.jobs[job.Name].DeliveryState)
	if !state.ReportReady || state.TaskCompleted {
		t.Fatalf("delivery state = %#v", state)
	}
	if repo.jobs[job.Name].Enabled {
		t.Fatal("completed=false report should still finish delivery")
	}
}

func TestConcurrentPlatformConnectionsGenerateOnce(t *testing.T) {
	ctx := context.Background()
	store := newFakeCronRepo()
	runner := &fakeCronRunner{text: `{"completed":true,"need_report":true,"report":"报告"}`}
	svc := newTestCronService(t, Options{Store: store, Runner: runner, EnabledPlatforms: []PlatformTarget{{Name: "qqonebot"}}, SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	meta := Metadata{Kind: metadataKind, Version: 2, Title: "并发", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{AllEnabledPlatforms: true, SourcePlatform: "cli"}}
	job := upsertTestCronJob(t, store, meta)

	var wg sync.WaitGroup
	for _, platformName := range []string{"qqonebot", "cli"} {
		platformName := platformName
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.handlePlatformConnected(ctx, platformName)
		}()
	}
	wg.Wait()
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d", runner.calls)
	}
	latest, err := store.GetByName(ctx, job.Name)
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if latest.Enabled {
		t.Fatal("job should be disabled after both platforms receive the report")
	}
}

func TestDisableDuringMissedDeliveryDoesNotReenableJob(t *testing.T) {
	ctx := context.Background()
	store := newFakeCronRepo()
	started := make(chan struct{})
	release := make(chan struct{})
	svc := newTestCronService(t, Options{Store: store, SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
		if outputs[0].Text == "提醒补发：\n\n正文" {
			close(started)
			<-release
		}
		return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
	}})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	meta := Metadata{Kind: metadataKind, Version: 2, Title: "提醒", CreatedBy: CronActor{Platform: "cli", PlatformUserID: "local"}, Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "正文"}, Target: CronTarget{SourcePlatform: "cli"}}
	job := upsertTestCronJob(t, store, meta)

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.handlePlatformConnected(ctx, "cli")
	}()
	<-started
	if err := svc.manager.DisableJob(ctx, job.Name); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	close(release)
	<-done

	latest, err := store.GetByName(ctx, job.Name)
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if latest.Enabled {
		t.Fatal("missed delivery re-enabled a disabled job")
	}
}

func TestRunLLMReportStartsFreshSessionEachCronTrigger(t *testing.T) {
	repo := newFakeCronRepo()
	runner := &fakeCronRunner{text: `{"completed":true,"need_report":false,"report":"ok"}`, sessionIDs: []string{"cron-session-1", "cron-session-2"}}
	svc := newTestCronService(t, Options{Store: repo, Runner: runner})
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "LLM", Schedule: CronSchedule{Mode: ScheduleCron, CronExpr: "*/5 * * * *"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}, LLM: CronLLMMetadata{ToolListNames: []string{"web_search"}}})
	meta := mustDecodeTestMetadata(t, job.Metadata)
	_, _, _, err := svc.runLLMReport(context.Background(), *job, meta, CronDeliveryState{RunID: "run-1"})
	if err != nil {
		t.Fatalf("first runLLMReport: %v", err)
	}
	_, _, _, err = svc.runLLMReport(context.Background(), *job, meta, CronDeliveryState{RunID: "run-2"})
	if err != nil {
		t.Fatalf("second runLLMReport: %v", err)
	}
	if len(runner.requests) != 2 {
		t.Fatalf("runner requests = %d", len(runner.requests))
	}
	if runner.requests[0].SessionID != "" || runner.requests[1].SessionID != "" {
		t.Fatalf("cron triggers should start fresh sessions: %#v", runner.requests)
	}
}

func TestRunLLMReportStoresGeneratedReport(t *testing.T) {
	repo := newFakeCronRepo()
	runner := &fakeCronRunner{text: `{"completed":true,"need_report":true,"report":"新报告"}`, sessionIDs: []string{"new-session"}}
	svc := newTestCronService(t, Options{Store: repo, Runner: runner})
	job := upsertTestCronJob(t, repo, Metadata{
		Kind:     metadataKind,
		Version:  1,
		Title:    "LLM",
		Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"},
		Trigger:  CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")},
		Target:   CronTarget{SourcePlatform: "cli"},
	})

	updated, report, _, err := svc.runLLMReport(context.Background(), *job, mustDecodeTestMetadata(t, job.Metadata), CronDeliveryState{RunID: "new-run"})
	if err != nil {
		t.Fatalf("runLLMReport: %v", err)
	}
	if runner.calls != 1 || runner.requests[0].SessionID != "" {
		t.Fatalf("runner requests = %#v", runner.requests)
	}
	if report != "新报告" || updated.Report != "新报告" || updated.ReportSessionID != "new-session" {
		t.Fatalf("report=%q delivery=%#v", report, updated)
	}
}

func TestRunLLMReportSendsNonEmptyReportEvenWhenNeedReportFalse(t *testing.T) {
	repo := newFakeCronRepo()
	runner := &fakeCronRunner{text: `{"completed":true,"need_report":false,"report":"仍然汇报"}`}
	svc := newTestCronService(t, Options{Store: repo, Runner: runner})
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "LLM", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}})

	meta := mustDecodeTestMetadata(t, job.Metadata)
	updated, report, _, err := svc.runLLMReport(context.Background(), *job, meta, CronDeliveryState{RunID: "run"})
	if err != nil {
		t.Fatalf("runLLMReport: %v", err)
	}
	if !runner.requests[0].NeedsElyphRules || !strings.Contains(runner.requests[0].Prompt, "#task test") || !strings.Contains(runner.requests[0].Prompt, "最终回复必须是严格 JSON") {

		t.Fatalf("cron prompt missing ELyph rules/task/json protocol: %q", runner.requests[0].Prompt)
	}
	if report != "仍然汇报" || updated.Report != "仍然汇报" {
		t.Fatalf("report=%q delivery=%q", report, updated.Report)
	}
}

func TestRunLLMReportRetriesInvalidJSONOnce(t *testing.T) {
	repo := newFakeCronRepo()
	runner := &fakeCronRunner{texts: []string{"不是 JSON", `{"completed":true,"need_report":true,"report":"修正后报告"}`}}
	svc := newTestCronService(t, Options{Store: repo, Runner: runner})
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "LLM", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}, LLM: CronLLMMetadata{ToolListNames: []string{"web_search"}}})

	meta := mustDecodeTestMetadata(t, job.Metadata)
	updated, report, _, err := svc.runLLMReport(context.Background(), *job, meta, CronDeliveryState{RunID: "run"})
	if err != nil {
		t.Fatalf("runLLMReport: %v", err)
	}
	if runner.calls != 2 {
		t.Fatalf("runner calls = %d", runner.calls)
	}
	if runner.requests[1].SessionID != "cron-session" {
		t.Fatalf("retry session id = %q", runner.requests[1].SessionID)
	}
	if len(runner.requests[1].ToolListNames) != 0 {
		t.Fatalf("retry tool list = %#v", runner.requests[1].ToolListNames)
	}
	if !strings.Contains(runner.requests[1].Prompt, "你返回的格式有误") {
		t.Fatalf("retry prompt = %q", runner.requests[1].Prompt)
	}
	if report != "修正后报告" || updated.Report != "修正后报告" {
		t.Fatalf("report=%q delivery=%q", report, updated.Report)
	}
}

func TestRunLLMSendsAdminNoticeWhenRetryStillInvalid(t *testing.T) {
	repo := newFakeCronRepo()
	runner := &fakeCronRunner{texts: []string{"不是 JSON", "仍然不是 JSON"}}
	var sent []string
	svc := newTestCronService(t, Options{
		Store:  repo,
		Runner: runner,
		SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
			sent = append(sent, target.Platform+":"+outputs[0].Text)
			return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
		},
	})
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "LLM", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerLLM, Message: testElyphTask("test")}, Target: CronTarget{SourcePlatform: "cli"}})

	meta := mustDecodeTestMetadata(t, job.Metadata)
	if _, err := svc.runLLM(context.Background(), *job, meta); err == nil {
		t.Fatal("expected parse error")
	}
	if runner.calls != 2 {
		t.Fatalf("runner calls = %d", runner.calls)
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "cli:cron 任务 LLM 解析格式失败") || !strings.Contains(sent[0], "请 /resume 到 cron session 查看详情") {
		t.Fatalf("sent = %#v", sent)
	}
}

func TestPlatformConnectedSkipsAlreadyDeliveredPlatform(t *testing.T) {
	repo := newFakeCronRepo()
	store := repo
	var sent []string
	svc := newTestCronService(t, Options{
		Store: store,
		SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
			sent = append(sent, target.Platform+":"+outputs[0].Text)
			return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
		},
	})
	svc.now = func() time.Time { return mustParseTestTime(t, "2026-01-02 03:05:00") }
	job := upsertTestCronJob(t, repo, Metadata{Kind: metadataKind, Version: 1, Title: "提醒", Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "该测试啦"}, Target: CronTarget{SourcePlatform: "cli"}})
	state := CronDeliveryState{RunID: "run", ReportReady: true, TaskCompleted: true, Report: "该测试啦", Targets: []CronDeliveryTargetState{{Key: "cli|superadmins", Outputs: []CronDeliveryOutputState{{ID: "text", Status: DeliveryDelivered}}}}}
	repo.jobs[job.Name].DeliveryState = mustMarshalTestDelivery(t, state)
	repo.jobs[job.Name].DeliveryToken = "run"

	startCronPlatformEvents(t, svc)

	emitCronPlatformAndWait(t, svc, "cli")
	if len(sent) != 0 {
		t.Fatalf("already delivered platform was sent again: %#v", sent)
	}
}

func TestCronSendAuditIncludesPlatform(t *testing.T) {
	var events []string
	connection, _ := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error {
		if record.Type == globalevents.ContentJSON {
			var content map[string]json.RawMessage
			if err := json.Unmarshal([]byte(record.Content), &content); err != nil {
				t.Fatal(err)
			}
			var platform string
			if err := json.Unmarshal(content["platform"], &platform); err == nil {
				events = append(events, record.EventName+":"+platform)
			}
		}
		return nil
	}, signal.ConnectOptions{})
	defer connection.Disconnect()
	svc := newTestCronService(t, Options{
		Store: newFakeCronRepo(),
		SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
			return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
		},
	})
	if _, err := svc.sendToPlatforms(context.Background(), "user.cron.audit", []string{"cli"}, "hello"); err != nil {
		t.Fatalf("sendToPlatforms: %v", err)
	}
	joined := strings.Join(events, "|")
	if !strings.Contains(joined, "cron.send_started:cli") || !strings.Contains(joined, "cron.send_completed:cli") {
		t.Fatalf("events = %#v", events)
	}
}

func newTestCronService(t *testing.T, opts Options) *Service {
	t.Helper()
	if opts.Store == nil {
		opts.Store = newFakeCronRepo()
	}
	if opts.Manager == nil {
		opts.Manager = NewManager(opts.Store)
	}
	if opts.Models == nil {
		opts.Models = &taskModels{current: config.ModelSelection{Provider: "provider", Model: "model"}}
	}
	if opts.SessionTakenOver == nil {
		opts.SessionTakenOver = func(context.Context, string) (bool, error) { return false, nil }
	}
	if opts.CopyBackground == nil {
		opts.CopyBackground = func(context.Context, agent.BackgroundCopyRequest) (string, error) { return "copy", nil }
	}
	if opts.SandboxRoot == "" {
		opts.SandboxRoot = t.TempDir()
	}
	s := NewService(opts)
	if err := opts.Manager.RegisterHandler(UserHandlerName, s.Handler); err != nil {
		t.Fatal(err)
	}
	return s
}

type fakeCronRunner struct {
	text       string
	texts      []string
	sessionIDs []string
	calls      int
	requests   []background.RunRequest
}

func (r *fakeCronRunner) RunBackground(ctx context.Context, req background.RunRequest) (background.RunResult, error) {
	r.requests = append(r.requests, req)
	text := r.text
	if r.calls < len(r.texts) {
		text = r.texts[r.calls]
	}
	sessionID := "cron-session"
	if r.calls < len(r.sessionIDs) {
		sessionID = r.sessionIDs[r.calls]
	}
	r.calls++
	return background.RunResult{SessionID: sessionID, MessageID: "cron-message", Text: text}, nil
}

func testElyphTask(name string) string {
	return "#task " + name + " - test task\n-> $report:str\n"
}

func upsertTestCronJob(t *testing.T, repo *fakeCronRepo, meta Metadata) *Job {
	t.Helper()
	job := &Job{ID: storage.NewID(), Name: "user.cron.test", Handler: UserHandlerName, Schedule: "4 3 2 1 *", Enabled: true, Metadata: mustMarshalTestMetadata(t, meta)}
	repo.mu.Lock()
	repo.jobs[job.Name] = job
	repo.mu.Unlock()
	copy := *job
	return &copy
}

func mustDecodeTestMetadata(t *testing.T, raw string) Metadata {
	t.Helper()
	meta, err := decodeMetadata(raw)
	if err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	return meta
}

func mustMarshalTestMetadata(t *testing.T, meta Metadata) string {
	t.Helper()
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	return string(data)
}

func mustDecodeTestDelivery(t *testing.T, raw string) CronDeliveryState {
	t.Helper()
	state, err := decodeDeliveryState(raw)
	if err != nil {
		t.Fatalf("decode delivery state: %v", err)
	}
	return state
}

func mustMarshalTestDelivery(t *testing.T, state CronDeliveryState) string {
	t.Helper()
	raw, err := encodeDeliveryState(state)
	if err != nil {
		t.Fatalf("encode delivery state: %v", err)
	}
	return raw
}

func deliveryPlatformDone(state CronDeliveryState, platformName string) bool {
	prefix := platformName + "|"
	found := false
	for _, target := range state.Targets {
		if !strings.HasPrefix(target.Key, prefix) {
			continue
		}
		found = true
		for _, output := range target.Outputs {
			if !deliveryStatusDone(output.Status) {
				return false
			}
		}
	}
	return found
}

func newCronSQLiteStore(t *testing.T) storage.Store {
	t.Helper()
	store := testutil.NewSQLiteStore(t)
	return store
}

func mustParseTestTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := parseRunAt(value)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return parsed
}

func deliverTestReport(t *testing.T, s *Service, platform, sessionID, messageID string) (globalevents.LogResult, error) {
	t.Helper()
	meta := Metadata{Kind: metadataKind, Schedule: CronSchedule{Mode: ScheduleOnce, RunAt: "2026-01-02 03:04:00"}, Trigger: CronTrigger{Mode: TriggerDirect, Message: "report"}, Target: CronTarget{SourcePlatform: platform}}
	job := upsertTestCronJob(t, s.store.(*fakeCronRepo), meta)
	state := CronDeliveryState{RunID: "run", ReportReady: true, Report: "report", ReportSessionID: sessionID, ReportMessageID: messageID}
	if ok, err := s.store.CompareAndSwapDelivery(t.Context(), job.ID, "", "run", mustMarshalTestDelivery(t, state), nil); err != nil || !ok {
		t.Fatalf("prepare delivery: %v %v", ok, err)
	}
	job.DeliveryToken = "run"
	return s.deliverPrepared(t.Context(), *job, meta, state, "", false)
}

func TestNormalizeJobNameAddsUserPrefix(t *testing.T) {
	if got := normalizeJobName(" daily summary! "); got != "user.cron.daily_summary" {
		t.Fatalf("name=%q", got)
	}
}
