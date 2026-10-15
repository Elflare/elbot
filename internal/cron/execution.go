package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/elyph"
	globalevents "elbot/internal/events"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func (s *Service) runDirect(ctx context.Context, job storage.CronJob, meta Metadata) (globalevents.LogResult, error) {
	content := "operation=runDirect"
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

func (s *Service) runLLM(ctx context.Context, job storage.CronJob, meta Metadata) (globalevents.LogResult, error) {
	content := "operation=runLLM"
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

func (s *Service) prepareDelivery(ctx context.Context, job storage.CronJob, meta Metadata) (storage.CronJob, CronDeliveryState, globalevents.LogResult, error) {
	var logResult globalevents.LogResult
	content := "operation=prepareDelivery"
	state, err := decodeDeliveryState(job.DeliveryState)
	if err != nil {
		{
			err := err
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content}
			if err != nil {
				logResult.Content += fmt.Sprintf("\nerror=%v", err)
			}
			return job, CronDeliveryState{}, logResult, err
		}
	}
	if meta.Schedule.Mode == ScheduleCron || strings.TrimSpace(job.DeliveryToken) == "" || strings.TrimSpace(state.RunID) == "" {
		state = CronDeliveryState{RunID: storage.NewID()}
		encoded, encodeErr := encodeDeliveryState(state)
		if encodeErr != nil {
			{
				err := encodeErr
				logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content}
				if err != nil {
					logResult.Content += fmt.Sprintf("\nerror=%v", err)
				}
				return job, CronDeliveryState{}, logResult, err
			}
		}
		swapped, swapErr := s.store.CronJobs().CompareAndSwapDelivery(ctx, job.ID, job.DeliveryToken, state.RunID, encoded, nil)
		if swapErr != nil {
			resultStatus := globalevents.ResultFailed
			if swapErr == context.Canceled {
				resultStatus = globalevents.ResultCanceled
			}
			{
				err := swapErr
				logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
				if err != nil {
					logResult.Content += fmt.Sprintf("\nerror=%v", err)
				}
				return job, CronDeliveryState{}, logResult, err
			}
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
		state.Report = strings.TrimSpace(meta.Trigger.Message)
	case TriggerLLM:
		state, _, logResult, err = s.runLLMReport(ctx, job, meta, state)
		resultStatus = logResult.ResultStatus
		content += "\n" + logResult.Content
	default:
		{
			err := fmt.Errorf("unsupported trigger mode %q", meta.Trigger.Mode)
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content}
			if err != nil {
				logResult.Content += fmt.Sprintf("\nerror=%v", err)
			}
			return job, state, logResult, err
		}
	}
	if meta.Trigger.Mode == TriggerLLM && !state.ReportReady {
		return job, state, globalevents.LogResult{ResultStatus: resultStatus, Content: content}, err
	}
	if state.TakenOver {
		ctx = context.WithoutCancel(ctx)
	}
	nextToken := firstNonEmpty(state.ReportSessionID, state.RunID)
	encoded, encodeErr := encodeDeliveryState(state)
	if encodeErr != nil {
		return job, state, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content + fmt.Sprintf("\nencode_error=%q", encodeErr.Error())}, errors.Join(err, encodeErr)
	}
	swapped, swapErr := s.store.CronJobs().CompareAndSwapDelivery(ctx, job.ID, job.DeliveryToken, nextToken, encoded, nil)
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

func (s *Service) runLLMReport(ctx context.Context, job storage.CronJob, meta Metadata, state CronDeliveryState) (CronDeliveryState, string, globalevents.LogResult, error) {
	var logResult globalevents.LogResult
	content := "operation=runLLMReport"

	model, err := s.modelForTask(meta.LLM)
	if err != nil {
		resultStatus := globalevents.ResultFailed
		if err == context.Canceled {
			resultStatus = globalevents.ResultCanceled
		}
		{
			err := err
			logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
			if err != nil {
				logResult.Content += fmt.Sprintf("\nerror=%v", err)
			}
			return state, "", logResult, err
		}
	}
	actor := contextinfo.Actor{ID: security.ActorID(meta.CreatedBy.Platform, meta.CreatedBy.PlatformUserID), Platform: meta.CreatedBy.Platform, PlatformUserID: meta.CreatedBy.PlatformUserID, DisplayName: meta.CreatedBy.DisplayName, Role: contextinfo.RoleSuperadmin}
	runRequest := background.RunRequest{Kind: background.KindCron, Name: job.Name, Title: meta.Title, Platform: meta.Target.SourcePlatform, Actor: actor, ScopeID: cronScopeID(job.Name), Prompt: cronPrompt(meta.Trigger.Message), SandboxSubdir: cronSandboxSubdir(job.Name), Metadata: map[string]string{"cron_job_name": job.Name}}
	runRequest.ToolListNames = meta.LLM.ToolListNames
	runRequest.ModelProvider, runRequest.Model = model.Provider, model.Model
	result, err := s.runner.RunBackground(ctx, runRequest)
	if taken, lookupErr := background.SessionTakenOver(context.WithoutCancel(ctx), s.store, result.SessionID); lookupErr != nil {
		resultStatus := globalevents.ResultFailed
		if lookupErr == context.Canceled {
			resultStatus = globalevents.ResultCanceled
		}
		{
			err := errors.Join(err, lookupErr)
			logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
			if err != nil {
				logResult.Content += fmt.Sprintf("\nerror=%v", err)
			}
			return state, "", logResult, err
		}
	} else if taken {
		result.TakenOver = true
	}
	if result.TakenOver {
		return takeoverState(state, result), "", globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: content + "\nreport generation skipped: execution taken over by foreground"}, err
	}
	if err != nil {
		// The report operation did not complete. Lower-level cancellation and
		// tool/model outcomes are recorded by their own sources, not inferred here.
		return state, "", globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content + fmt.Sprintf("\nreport generation failed: %v", err)}, err
	}
	resultStatus := globalevents.ResultSucceeded
	parsed, err := parseLLMResult(result.Text)
	if err != nil {
		result, parsed, logResult, err = s.retryLLMResultFormat(ctx, job, meta, actor, result.SessionID, model)
		resultStatus = logResult.ResultStatus
		content += "\n" + logResult.Content
	}
	if result.TakenOver {
		{
			err := err
			logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
			return takeoverState(state, result), "", logResult, err
		}
	}
	if err != nil {
		message := cronParseFailedMessage(meta.Title, result.SessionID, err)
		state.ReportReady = true
		state.Report = message
		state.ReportSessionID = result.SessionID
		state.ReportMessageID = result.MessageID
		{
			err := err
			logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
			return state, message, logResult, err
		}
	}
	var copyErr error
	if meta.Target.AllEnabledPlatforms {
		copyErr = s.copySessionToBroadcastTargets(ctx, result.SessionID, meta, job.Name)
		if err := copyErr; err != nil {
			if errors.Is(err, session.ErrForegroundSession) {
				result.TakenOver = true
				return takeoverState(state, result), "", globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: content}, nil
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
				facts, _ := contextinfo.ExecutionFromContext(ctx)
				if facts.SessionID != "" {
					record.Identity.SessionID = &facts.SessionID
				}
				if facts.RunID != "" {
					record.Identity.RunID = &facts.RunID
				}
				if facts.Attempt != "" {
					record.Identity.Attempt = &facts.Attempt
				}
				if facts.RequestID != "" {
					record.Identity.RequestID = &facts.RequestID
				}
				if facts.RootRequestID != "" {
					record.Identity.RootRequestID = &facts.RootRequestID
				}
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
	state.ReportSegments = parsed.ReportSegments
	state.ReportSessionID = result.SessionID
	state.ReportMessageID = result.MessageID
	report := strings.TrimSpace(parsed.Report)
	if parsed.Completed {
		state.Report = report
		return state, report, globalevents.LogResult{ResultStatus: resultStatus, Content: content}, copyErr
	}
	if report == "" {
		report = "任务未完成。"
	}
	report += fmt.Sprintf("\n可 /resume 到 cron session 查看详情。\nsession: %s", result.SessionID)
	state.Report = report
	return state, report, globalevents.LogResult{ResultStatus: resultStatus, Content: content}, copyErr
}

func (s *Service) retryLLMResultFormat(ctx context.Context, job storage.CronJob, meta Metadata, actor contextinfo.Actor, sessionID string, model config.ModelSelection) (background.RunResult, CronLLMResult, globalevents.LogResult, error) {
	content := "operation=retryLLMResultFormat"
	if taken, err := background.SessionTakenOver(ctx, s.store, sessionID); err != nil || taken {
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
	parsed, err := parseLLMResult(result.Text)
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

` + elyph.RuleCard() + `

** “Cron 任务内容”必须是 ELyph #task <name> - 描述 任务文本
** 按“Cron 任务内容”中的 ELyph 任务自主执行
** Cron 任务内容不需要包含最终 JSON 格式或汇报字段要求
** 信息不足时，在最终 JSON 的 report 填写失败或阻塞原因
** 只能调用本任务提供的工具；没有提供工具时，基于已有信息完成或报告阻塞
** 所有路径参数必须使用相对路径，基于当前任务工作目录解析；不要使用绝对路径、~、.. 或 cd。
** 有投递目标、任务要求通知或产生需要目标知道的结果/失败/阻塞原因时，应设置 need_report=true 并在 report 写自然语言汇报
** 最终回复必须是严格 JSON
** JSON 格式：{"completed":true,"need_report":false,"report":"","report_segments":[]}
** report_segments 可选数组，元素为 {"type":"image|file","url":"相对路径或 HTTP(S) URL"}，用于附带图片或文件。本地图片/文件须先保存在当前任务工作目录内；远程 URL 会原样交给平台，不会下载
** completed 表示是否完成任务
** need_report 表示是否需要向目标平台汇报；成功、失败或阻塞都可以请求汇报
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
	return background.DefaultJSONRetryPrompt()
}

func parseLLMResult(text string) (CronLLMResult, error) {
	return background.ParseJSONResult(text)
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
