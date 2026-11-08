package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/agent"
	background "elbot/internal/background/share"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
)

func (s *Service) runTask(ctx context.Context, job Job, meta Metadata) (context.Context, error) {
	ctx, job, state, err := s.prepareDelivery(ctx, job, meta)
	if err != nil || !state.ReportReady {
		return ctx, err
	}
	if state.TakenOver {
		ctx = context.WithoutCancel(ctx)
	}
	return ctx, s.deliverPrepared(ctx, job, meta, state, "", false)
}

func (s *Service) prepareDelivery(ctx context.Context, job Job, meta Metadata) (context.Context, Job, CronDeliveryState, error) {

	if len(s.targetPlatformNames(meta)) == 0 {
		err := fmt.Errorf("no allowed delivery targets: source_platform=%q all_enabled_platforms=%t enabled_platforms=%v; target platform is not enabled", meta.Target.SourcePlatform, meta.Target.AllEnabledPlatforms, s.enabledPlatformNames())
		return ctx, job, CronDeliveryState{}, err
	}
	state, err := decodeDeliveryState(job.DeliveryState)
	if err != nil {
		return ctx, job, CronDeliveryState{}, err
	}
	if meta.Schedule.Mode == ScheduleCron || strings.TrimSpace(job.DeliveryToken) == "" || strings.TrimSpace(state.RunID) == "" {
		id, err := s.newRunID(ctx)
		if err != nil {
			return ctx, job, state, err
		}
		state = CronDeliveryState{RunID: id}
		encoded := encodeDeliveryState(state)
		swapped, swapErr := s.store.CompareAndSwapDelivery(ctx, job.ID, job.DeliveryToken, state.RunID, encoded, nil)
		if swapErr != nil {
			return ctx, job, CronDeliveryState{}, swapErr
		}
		if !swapped {
			return ctx, job, CronDeliveryState{}, nil
		}
		job.DeliveryToken, job.DeliveryState = state.RunID, encoded
	}
	if state.ReportReady {
		return ctx, job, state, nil
	}

	switch meta.Trigger.Mode {
	case TriggerDirect:
		state.ReportReady, state.TaskCompleted = true, true
		state.NeedReport = true
		state.Report = strings.TrimSpace(meta.Trigger.Message)
	case TriggerLLM:
		ctx, state, err = s.runLLMReport(ctx, job, meta, state)

	default:
		err := fmt.Errorf("unsupported trigger mode %q", meta.Trigger.Mode)
		return ctx, job, state, err
	}
	if meta.Trigger.Mode == TriggerLLM && !state.ReportReady {
		return ctx, job, state, err
	}
	if state.TakenOver {
		ctx = context.WithoutCancel(ctx)
	}
	nextToken := firstNonEmpty(state.ReportSessionID, state.RunID)
	state.DeliveryComplete = s.deliveryComplete(meta, state)
	encoded := encodeDeliveryState(state)
	swapped, swapErr := s.store.CompareAndSwapDelivery(ctx, job.ID, job.DeliveryToken, nextToken, encoded, nil)
	if swapErr != nil {
		return ctx, job, state, swapErr
	}
	if !swapped {
		return ctx, job, CronDeliveryState{}, err
	}
	job.DeliveryToken, job.DeliveryState = nextToken, encoded
	return ctx, job, state, err
}

func (s *Service) runLLMReport(ctx context.Context, job Job, meta Metadata, state CronDeliveryState) (context.Context, CronDeliveryState, error) {

	platforms := s.targetPlatformNames(meta)

	model, err := s.modelForTask(meta.LLM)
	if err != nil {
		return ctx, state, err
	}
	actor := contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: meta.CreatedBy.Platform, ID: meta.CreatedBy.ID}, Name: meta.CreatedBy.Name, Role: contextinfo.RoleSuperadmin}
	runRequest := background.RunRequest{TaskType: background.KindCron, TaskID: job.Name, Name: meta.Title, Actor: actor, Prompt: cronPrompt(meta.Trigger.Message), SandboxSubdir: cronSandboxSubdir(job.Name), Metadata: map[string]string{"cron_job_name": job.Name}}
	runRequest.TargetPlatforms = platforms
	runRequest.NeedsElyphRules = true
	runRequest.ToolListNames = meta.LLM.ToolListNames
	runRequest.ModelProvider, runRequest.Model = model.Provider, model.Model
	ctx, result, err := s.runner.RunBackground(ctx, runRequest)
	if err != nil && !result.TakenOver {
		return ctx, state, err
	}
	if taken, lookupErr := s.sessionTakenOver(context.WithoutCancel(ctx), result.SessionID); lookupErr != nil {
		return ctx, state, lookupErr
	} else if taken {
		result.TakenOver = true
	}
	if result.TakenOver {
		return ctx, takeoverState(state, result), err
	}
	if err != nil {
		// The report operation did not complete. Lower-level cancellation and
		// tool/model outcomes are recorded by their own sources, not inferred here.
		return ctx, state, err
	}

	parsed, err := background.ParseJSONResult(result.Text)
	if err != nil {
		globalevents.AsyncLog(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: slog.LevelWarn, EventName: "cron_format_retry", Module: "cron", Content: err.Error(), Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.LogStatus(err)})
		ctx, result, err = s.retryLLMResultFormat(ctx, job, meta, actor, result.SessionID, model)
		if err != nil {
			return ctx, state, err
		}
		if result.TakenOver {
			return ctx, takeoverState(state, result), nil
		}
		parsed, err = background.ParseJSONResult(result.Text)
	}
	if err != nil {
		message := cronParseFailedMessage(meta.Title, result.SessionID, err)
		state.ReportReady = true
		state.NeedReport = true
		state.Report = message
		state.ReportSessionID = result.SessionID
		state.ReportMessageID = result.MessageID
		globalevents.AsyncLog(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: slog.LevelWarn, EventName: "cron_format_failed", Module: "cron", Content: err.Error(), Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.LogStatus(err)})
		return ctx, state, nil
	}
	var copyErr error
	if meta.Target.AllEnabledPlatforms {
		copyErr = s.copySessionToBroadcastTargets(ctx, result.SessionID, meta, job.Name)
		if err := copyErr; err != nil {
			if errors.Is(err, agent.ErrForegroundSession) {
				result.TakenOver = true
				return ctx, takeoverState(state, result), nil
			}
			record := globalevents.LogRecord{
				Category:     globalevents.LogCategoryLog,
				Level:        slog.LevelError,
				EventName:    "copy_cron_session_failed",
				Module:       "cron",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("copy cron session failed\njob=%v\nerror=%v", job.Name, err),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			record.Identity = globalevents.LogIdentityFromContext(ctx)
			globalevents.Log(ctx, record)
		}
	}
	state.ReportReady = true
	state.TaskCompleted = parsed.Completed
	state.NeedReport = parsed.NeedReport
	state.ReportSegments = parsed.ReportSegments
	state.ReportSessionID = result.SessionID
	state.ReportMessageID = result.MessageID
	report := strings.TrimSpace(parsed.Report)
	if parsed.Completed {
		state.Report = report
		return ctx, state, nil
	}
	if report == "" {
		report = "任务未完成。"
	}
	report += fmt.Sprintf("\n可 /resume 到 cron session 查看详情。\nsession: %s", result.SessionID)
	state.Report = report
	return ctx, state, nil
}

func (s *Service) retryLLMResultFormat(ctx context.Context, job Job, meta Metadata, actor contextinfo.Actor, sessionID string, model config.ModelSelection) (context.Context, background.RunResult, error) {

	if taken, err := s.sessionTakenOver(ctx, sessionID); err != nil || taken {
		return ctx, background.RunResult{SessionID: sessionID, TakenOver: taken, Outcome: "taken_over"}, err
	}
	runRequest := background.RunRequest{TaskType: background.KindCron, TaskID: job.Name, Name: meta.Title, Actor: actor, SessionID: sessionID, Prompt: cronFormatRetryPrompt(), SandboxSubdir: cronSandboxSubdir(job.Name), Metadata: map[string]string{"cron_job_name": job.Name}}
	runRequest.ModelProvider, runRequest.Model = model.Provider, model.Model
	return s.runner.RunBackground(ctx, runRequest)
}

func cronParseFailedMessage(title, sessionID string, err error) string {
	return fmt.Sprintf("cron 任务 %s 解析格式失败，请 /resume 到 cron session 查看详情。\nsession: %s\n错误：%v", title, sessionID, err)
}

func cronPrompt(message string) string {
	return `[系统 Cron 后台任务]

** “Cron 任务内容”必须是 ELyph #task <name> - 描述 任务文本
** 按“Cron 任务内容”中的 ELyph 任务自主执行
** Cron 任务内容不需要包含最终 JSON 格式或汇报字段要求
** 信息不足时，在最终 JSON 的 report 填写失败或阻塞原因
** 只能调用本任务提供的工具；没有提供工具时，基于已有信息完成或报告阻塞
** 所有路径参数必须使用相对路径，基于当前任务工作目录解析；不要使用绝对路径、~、.. 或 cd。
** 需要向目标说明处理结果、失败或阻塞原因时，应设置 need_report=true 并在 report 写自然语言汇报；仅发送附件时可设置 need_report=false
** 最终回复必须是严格 JSON
** JSON 格式：{"completed":true,"need_report":false,"report":"","report_segments":[]}
** report_segments 可选数组，元素为 {"type":"image|file","url":"相对路径或 HTTP(S) URL"}，用于附带图片或文件。本地图片/文件须先保存在当前任务工作目录内；远程 URL 会原样交给平台，不会下载
** completed 表示是否完成任务
** need_report 只控制是否向目标平台发送 report 正文；成功、失败或阻塞都可以请求汇报。report_segments 附件始终保存并发送，不受 need_report 影响
** report 为需要发给用户的汇报，可填写处理结果、失败原因或阻塞原因
~ 把任务当作前台用户对话
~ 闲聊
~ 向用户提问
~ 输出 Markdown 代码块
~ 输出 JSON 外的任何文字

Cron 任务内容：
` + strings.TrimSpace(message)
}

func cronFormatRetryPrompt() string {
	return background.DefaultJSONRetryPrompt() + "在 Cron 中，need_report 只控制 report 正文是否发送；report_segments 附件始终保存并发送，不受 need_report 影响。"
}

func takeoverState(state CronDeliveryState, result background.RunResult) CronDeliveryState {
	state.TakenOver = true
	state.ExecutionRunID = result.RunID
	state.Outcome = result.Outcome
	state.ExecutionResult = result.Text
	state.ReportReady = true
	state.TaskCompleted = false
	state.Report = ""
	state.ReportSegments = nil
	state.ReportSessionID = result.SessionID
	state.ReportMessageID = result.MessageID
	return state
}
