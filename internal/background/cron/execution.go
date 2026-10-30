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
	"elbot/internal/security"
	"elbot/internal/storage"
)

func (s *Service) runTask(ctx context.Context, job Job, meta Metadata) (globalevents.LogResult, error) {
	content := "operation=runLLM"
	if meta.Trigger.Mode == TriggerDirect {
		content = "operation=runDirect"
	}
	job, state, logResult, prepareErr := s.prepareDelivery(ctx, job, meta)
	resultStatus := logResult.ResultStatus
	content += "\n" + logResult.Content
	if !state.ReportReady {
		return globalevents.LogResult{ResultStatus: resultStatus, Content: content}, prepareErr
	}
	if state.TakenOver {
		ctx = context.WithoutCancel(ctx)
	}
	logResult, deliverErr := s.deliverPrepared(ctx, job, meta, state, "", false)
	status := logResult.ResultStatus
	content += "\n" + logResult.Content
	if resultStatus == globalevents.ResultFailed || status == globalevents.ResultFailed {
		status = globalevents.ResultFailed
	} else if resultStatus == globalevents.ResultCanceled || status == globalevents.ResultCanceled {
		status = globalevents.ResultCanceled
	}
	return globalevents.LogResult{ResultStatus: status, Content: content}, errors.Join(prepareErr, deliverErr)
}

func (s *Service) prepareDelivery(ctx context.Context, job Job, meta Metadata) (Job, CronDeliveryState, globalevents.LogResult, error) {
	var logResult globalevents.LogResult
	content := "operation=prepareDelivery"
	state, err := decodeDeliveryState(job.DeliveryState)
	if err != nil {
		return job, CronDeliveryState{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content + fmt.Sprintf("\nerror=%v", err)}, err
	}
	if meta.Schedule.Mode == ScheduleCron || strings.TrimSpace(job.DeliveryToken) == "" || strings.TrimSpace(state.RunID) == "" {
		state = CronDeliveryState{RunID: storage.NewID()}
		encoded, encodeErr := encodeDeliveryState(state)
		if encodeErr != nil {
			return job, CronDeliveryState{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content + fmt.Sprintf("\nerror=%v", encodeErr)}, encodeErr
		}
		swapped, swapErr := s.store.CompareAndSwapDelivery(ctx, job.ID, job.DeliveryToken, state.RunID, encoded, nil)
		if swapErr != nil {
			resultStatus := globalevents.ResultFailed
			if swapErr == context.Canceled {
				resultStatus = globalevents.ResultCanceled
			}
			return job, CronDeliveryState{}, globalevents.LogResult{ResultStatus: resultStatus, Content: content + fmt.Sprintf("\nerror=%v", swapErr)}, swapErr
		}
		if !swapped {
			return job, CronDeliveryState{}, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: content}, nil
		}
		job.DeliveryToken, job.DeliveryState = state.RunID, encoded
	}
	if state.ReportReady {
		return job, state, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: content}, nil
	}
	resultStatus := globalevents.ResultSucceeded
	switch meta.Trigger.Mode {
	case TriggerDirect:
		state.ReportReady, state.TaskCompleted = true, true
		state.NeedReport = true
		state.Report = strings.TrimSpace(meta.Trigger.Message)
	case TriggerLLM:
		state, logResult, err = s.runLLMReport(ctx, job, meta, state)
		resultStatus = logResult.ResultStatus
		content += "\n" + logResult.Content
	default:
		err := fmt.Errorf("unsupported trigger mode %q", meta.Trigger.Mode)
		return job, state, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content + fmt.Sprintf("\nerror=%v", err)}, err
	}
	if meta.Trigger.Mode == TriggerLLM && !state.ReportReady {
		return job, state, globalevents.LogResult{ResultStatus: resultStatus, Content: content}, err
	}
	if state.TakenOver {
		ctx = context.WithoutCancel(ctx)
	}
	nextToken := firstNonEmpty(state.ReportSessionID, state.RunID)
	state.DeliveryComplete = s.deliveryComplete(meta, state)
	encoded, encodeErr := encodeDeliveryState(state)
	if encodeErr != nil {
		return job, state, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content + fmt.Sprintf("\nencode_error=%q", encodeErr.Error())}, errors.Join(err, encodeErr)
	}
	swapped, swapErr := s.store.CompareAndSwapDelivery(ctx, job.ID, job.DeliveryToken, nextToken, encoded, nil)
	if swapErr != nil {
		if swapErr != context.Canceled {
			resultStatus = globalevents.ResultFailed
		} else if resultStatus != globalevents.ResultFailed {
			resultStatus = globalevents.ResultCanceled
		}
		return job, state, globalevents.LogResult{ResultStatus: resultStatus, Content: content + fmt.Sprintf("\nsave_error=%q", swapErr.Error())}, errors.Join(err, swapErr)
	}
	if !swapped {
		return job, CronDeliveryState{}, globalevents.LogResult{ResultStatus: resultStatus, Content: content}, err
	}
	job.DeliveryToken, job.DeliveryState = nextToken, encoded
	return job, state, globalevents.LogResult{ResultStatus: resultStatus, Content: content}, err
}

func (s *Service) runLLMReport(ctx context.Context, job Job, meta Metadata, state CronDeliveryState) (CronDeliveryState, globalevents.LogResult, error) {
	var logResult globalevents.LogResult
	content := "operation=runLLMReport"

	model, err := s.modelForTask(meta.LLM)
	if err != nil {
		resultStatus := globalevents.ResultFailed
		if err == context.Canceled {
			resultStatus = globalevents.ResultCanceled
		}
		return state, globalevents.LogResult{ResultStatus: resultStatus, Content: content + fmt.Sprintf("\nerror=%v", err)}, err
	}
	actor := contextinfo.Actor{ID: security.ActorID(meta.CreatedBy.Platform, meta.CreatedBy.PlatformUserID), Platform: meta.CreatedBy.Platform, PlatformUserID: meta.CreatedBy.PlatformUserID, DisplayName: meta.CreatedBy.DisplayName, Role: contextinfo.RoleSuperadmin}
	runRequest := background.RunRequest{Kind: background.KindCron, Name: job.Name, Title: meta.Title, Platform: meta.Target.SourcePlatform, Actor: actor, ScopeID: cronScopeID(job.Name), Prompt: cronPrompt(meta.Trigger.Message), SandboxSubdir: cronSandboxSubdir(job.Name), Metadata: map[string]string{"cron_job_name": job.Name}}
	runRequest.NeedsElyphRules = true
	runRequest.ToolListNames = meta.LLM.ToolListNames
	runRequest.ModelProvider, runRequest.Model = model.Provider, model.Model
	result, err := s.runner.RunBackground(ctx, runRequest)
	if taken, lookupErr := s.sessionTakenOver(context.WithoutCancel(ctx), result.SessionID); lookupErr != nil {
		resultStatus := globalevents.ResultFailed
		if lookupErr == context.Canceled {
			resultStatus = globalevents.ResultCanceled
		}
		err := errors.Join(err, lookupErr)
		return state, globalevents.LogResult{ResultStatus: resultStatus, Content: content + fmt.Sprintf("\nerror=%v", err)}, err
	} else if taken {
		result.TakenOver = true
	}
	if result.TakenOver {
		return takeoverState(state, result), globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: content + "\nreport generation skipped: execution taken over by foreground"}, err
	}
	if err != nil {
		// The report operation did not complete. Lower-level cancellation and
		// tool/model outcomes are recorded by their own sources, not inferred here.
		return state, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content + fmt.Sprintf("\nreport generation failed: %v", err)}, err
	}
	resultStatus := globalevents.ResultSucceeded
	parsed, err := background.ParseJSONResult(result.Text)
	if err != nil {
		result, parsed, logResult, err = s.retryLLMResultFormat(ctx, job, meta, actor, result.SessionID, model)
		resultStatus = logResult.ResultStatus
		content += "\n" + logResult.Content
	}
	if result.TakenOver {
		return takeoverState(state, result), globalevents.LogResult{ResultStatus: resultStatus, Content: content}, err
	}
	if err != nil {
		message := cronParseFailedMessage(meta.Title, result.SessionID, err)
		state.ReportReady = true
		state.NeedReport = true
		state.Report = message
		state.ReportSessionID = result.SessionID
		state.ReportMessageID = result.MessageID
		return state, globalevents.LogResult{ResultStatus: resultStatus, Content: content}, err
	}
	var copyErr error
	if meta.Target.AllEnabledPlatforms {
		copyErr = s.copySessionToBroadcastTargets(ctx, result.SessionID, meta, job.Name)
		if err := copyErr; err != nil {
			if errors.Is(err, agent.ErrForegroundSession) {
				result.TakenOver = true
				return takeoverState(state, result), globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: content}, nil
			}
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelError,
					EventName:    "copy_cron_session_failed",
					Module:       "cron",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("copy cron session failed\njob=%v\nerror=%v", job.Name, err),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				record.Identity = cronLogIdentity(ctx)
				background.LogBackground(ctx, record)
			}
		}
	}
	if copyErr != nil {
		resultStatus = globalevents.ResultFailed
		content += fmt.Sprintf("\ncopy_error=%v", copyErr)
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
		return state, globalevents.LogResult{ResultStatus: resultStatus, Content: content}, copyErr
	}
	if report == "" {
		report = "任务未完成。"
	}
	report += fmt.Sprintf("\n可 /resume 到 cron session 查看详情。\nsession: %s", result.SessionID)
	state.Report = report
	return state, globalevents.LogResult{ResultStatus: resultStatus, Content: content}, copyErr
}

func (s *Service) retryLLMResultFormat(ctx context.Context, job Job, meta Metadata, actor contextinfo.Actor, sessionID string, model config.ModelSelection) (background.RunResult, CronLLMResult, globalevents.LogResult, error) {
	content := "operation=retryLLMResultFormat"
	if taken, err := s.sessionTakenOver(ctx, sessionID); err != nil || taken {
		resultStatus := globalevents.ResultSkipped
		if err != nil {
			resultStatus = globalevents.ResultFailed
			content += fmt.Sprintf("\ntakeover lookup failed: %v", err)
			if err == context.Canceled {
				resultStatus = globalevents.ResultCanceled
			}
		} else {
			content += "\nformat repair skipped: execution taken over by foreground"
		}
		return background.RunResult{SessionID: sessionID, TakenOver: taken, Outcome: "taken_over"}, CronLLMResult{}, globalevents.LogResult{ResultStatus: resultStatus, Content: content}, err
	}
	runRequest := background.RunRequest{Kind: background.KindCron, Name: job.Name, Title: meta.Title, Platform: meta.Target.SourcePlatform, Actor: actor, ScopeID: cronScopeID(job.Name), SessionID: sessionID, Prompt: cronFormatRetryPrompt(), SandboxSubdir: cronSandboxSubdir(job.Name), Metadata: map[string]string{"cron_job_name": job.Name}}
	runRequest.ModelProvider, runRequest.Model = model.Provider, model.Model
	result, err := s.runner.RunBackground(ctx, runRequest)
	if err != nil {
		return result, CronLLMResult{}, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content + fmt.Sprintf("\nformat repair did not complete: %v", err)}, err
	}
	if result.SessionID == "" {
		result.SessionID = sessionID
	}
	if result.TakenOver {
		return result, CronLLMResult{}, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: content + "\nformat repair skipped: execution taken over by foreground"}, nil
	}
	parsed, err := background.ParseJSONResult(result.Text)
	if err != nil {
		return result, parsed, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content + fmt.Sprintf("\nparse error: %v", err)}, err
	}
	return result, parsed, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: content}, nil
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
