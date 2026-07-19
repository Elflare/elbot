package cron

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/background"
	"elbot/internal/elyph"
	"elbot/internal/security"
	"elbot/internal/storage"
)

func (s *Service) runDirect(ctx context.Context, job storage.CronJob, meta Metadata) error {
	job, state, prepareErr := s.prepareDelivery(ctx, job, meta)
	if !state.ReportReady {
		return prepareErr
	}
	return errors.Join(prepareErr, s.deliverPrepared(ctx, job, meta, state, "", false))
}

func (s *Service) runLLM(ctx context.Context, job storage.CronJob, meta Metadata) error {
	job, state, prepareErr := s.prepareDelivery(ctx, job, meta)
	if !state.ReportReady {
		return prepareErr
	}
	return errors.Join(prepareErr, s.deliverPrepared(ctx, job, meta, state, "", false))
}

func (s *Service) prepareDelivery(ctx context.Context, job storage.CronJob, meta Metadata) (storage.CronJob, CronDeliveryState, error) {
	state, err := decodeDeliveryState(job.DeliveryState)
	if err != nil {
		return job, CronDeliveryState{}, err
	}
	if meta.Schedule.Mode == ScheduleCron || strings.TrimSpace(job.DeliveryToken) == "" || strings.TrimSpace(state.RunID) == "" {
		state = CronDeliveryState{RunID: storage.NewID()}
		encoded, encodeErr := encodeDeliveryState(state)
		if encodeErr != nil {
			return job, CronDeliveryState{}, encodeErr
		}
		swapped, swapErr := s.store.CronJobs().CompareAndSwapDelivery(ctx, job.ID, job.DeliveryToken, state.RunID, encoded)
		if swapErr != nil {
			return job, CronDeliveryState{}, swapErr
		}
		if !swapped {
			return job, CronDeliveryState{}, nil
		}
		job.DeliveryToken = state.RunID
		job.DeliveryState = encoded
	}
	if state.ReportReady {
		return job, state, nil
	}
	switch meta.Trigger.Mode {
	case TriggerDirect:
		state.ReportReady = true
		state.TaskCompleted = true
		state.Report = strings.TrimSpace(meta.Trigger.Message)
	case TriggerLLM:
		state, _, err = s.runLLMReport(ctx, job, meta, state)
	default:
		return job, state, fmt.Errorf("unsupported trigger mode %q", meta.Trigger.Mode)
	}
	if meta.Trigger.Mode == TriggerLLM && !state.ReportReady {
		return job, state, err
	}
	nextToken := firstNonEmpty(state.ReportSessionID, state.RunID)
	encoded, encodeErr := encodeDeliveryState(state)
	if encodeErr != nil {
		return job, state, errors.Join(err, encodeErr)
	}
	swapped, swapErr := s.store.CronJobs().CompareAndSwapDelivery(ctx, job.ID, job.DeliveryToken, nextToken, encoded)
	if swapErr != nil {
		return job, state, errors.Join(err, swapErr)
	}
	if !swapped {
		return job, CronDeliveryState{}, err
	}
	job.DeliveryToken = nextToken
	job.DeliveryState = encoded
	return job, state, err
}

func (s *Service) runLLMReport(ctx context.Context, job storage.CronJob, meta Metadata, state CronDeliveryState) (CronDeliveryState, string, error) {
	if s.runner == nil {
		return state, "", fmt.Errorf("cron llm runner is not configured")
	}
	actor := security.Actor{ID: security.ActorID(meta.CreatedBy.Platform, meta.CreatedBy.PlatformUserID), Platform: meta.CreatedBy.Platform, PlatformUserID: meta.CreatedBy.PlatformUserID, DisplayName: meta.CreatedBy.DisplayName, Role: security.RoleSuperadmin}
	result, err := s.runner.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: job.Name, Title: meta.Title, Platform: meta.Target.SourcePlatform, Actor: actor, ScopeID: cronScopeID(job.Name), Prompt: cronPrompt(meta.Trigger.Message), ToolListNames: meta.LLM.ToolListNames, SessionMode: meta.LLM.SessionMode, SandboxSubdir: cronSandboxSubdir(job.Name), Metadata: map[string]string{"cron_job_name": job.Name}})
	if err != nil {
		return state, "", err
	}
	parsed, err := parseLLMResult(result.Text)
	if err != nil {
		result, parsed, err = s.retryLLMResultFormat(ctx, job, meta, actor, result.SessionID)
	}
	if err != nil {
		message := cronParseFailedMessage(meta.Title, result.SessionID, err)
		state.ReportReady = true
		state.Report = message
		state.ReportSessionID = result.SessionID
		state.ReportMessageID = result.MessageID
		return state, message, err
	}
	if meta.Target.AllEnabledPlatforms {
		if err := s.copySessionToBroadcastTargets(ctx, result.SessionID, meta, job.Name); err != nil {
			s.logWarn("copy cron session failed", "job", job.Name, "error", err)
		}
	}
	state.ReportReady = true
	state.TaskCompleted = parsed.Completed
	state.ReportSegments = parsed.ReportSegments
	state.ReportSessionID = result.SessionID
	state.ReportMessageID = result.MessageID
	report := strings.TrimSpace(parsed.Report)
	if parsed.Completed {
		state.Report = report
		return state, report, nil
	}
	if report == "" {
		report = "任务未完成。"
	}
	report += fmt.Sprintf("\n可 /resume 到 cron session 查看详情。\nsession: %s", result.SessionID)
	state.Report = report
	return state, report, nil
}

func (s *Service) retryLLMResultFormat(ctx context.Context, job storage.CronJob, meta Metadata, actor security.Actor, sessionID string) (background.RunResult, CronLLMResult, error) {
	result, err := s.runner.RunBackground(ctx, background.RunRequest{Kind: background.KindCron, Name: job.Name, Title: meta.Title, Platform: meta.Target.SourcePlatform, Actor: actor, ScopeID: cronScopeID(job.Name), SessionID: sessionID, Prompt: cronFormatRetryPrompt(), ToolListNames: meta.LLM.ToolListNames, SessionMode: meta.LLM.SessionMode, SandboxSubdir: cronSandboxSubdir(job.Name), Metadata: map[string]string{"cron_job_name": job.Name}})
	if err != nil {
		return result, CronLLMResult{}, err
	}
	if result.SessionID == "" {
		result.SessionID = sessionID
	}
	parsed, err := parseLLMResult(result.Text)
	return result, parsed, err
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
** 需要使用工具时直接使用工具
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
