package elnis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	background "elbot/internal/background/share"
	"elbot/internal/config"
	"elbot/internal/elyph"
	globalevents "elbot/internal/events"
)

// RunLLMEvent 执行已通过 prepareEvent 的入队事件；有效投递目标已在入口保证非空。
func (s *Service) RunLLMEvent(ctx context.Context, event Event, eventID string) (err error) {
	identity := globalevents.LogIdentityFromContext(ctx)
	// This entrypoint owns terminal reports; handled failures end here before state updates.
	logFailure := func(operation string, err error, level slog.Level) {
		identity := globalevents.LogIdentityFromContext(ctx)
		content, _ := json.Marshal(map[string]any{"event_id": eventID, "event_key": event.EventKey, "operation": operation, "error": err.Error()})
		record := globalevents.LogRecord{Category: globalevents.LogElnis, Level: level, Module: "elnis", EventName: "elnis_llm_failed", Type: globalevents.ContentJSON, Content: string(content), Identity: identity, ResultStatus: globalevents.LogStatus(err)}
		if record.ResultStatus == globalevents.ResultCanceled {
			record.Level = slog.LevelInfo
		}
		globalevents.Log(ctx, record)
	}
	defer func() {
		if err != nil {
			logFailure("run", err, slog.LevelError)
		}
	}()
	if err := s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusRunning, "", ""); err != nil {
		return err
	}

	{
		content, _ := json.Marshal(map[string]any{"origin": event.Origin, "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "event_id": eventID})
		record := globalevents.LogRecord{
			Category:  globalevents.LogAudit,
			Level:     slog.LevelInfo,
			EventName: "elnis.llm_started",
			Module:    "elnis",
			Type:      globalevents.ContentJSON,
			Content:   string(content),
		}
		record.Identity = identity
		globalevents.Log(ctx, record)
		record.Category, record.EventName = globalevents.LogElnis, "elnis_llm_started"
		globalevents.Log(ctx, record)
	}

	segments, err := s.materializeSegments(ctx, event.Segments, eventID)
	if err != nil {
		logFailure("task", err, slog.LevelError)
		return s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusFailed, "", err.Error())
	}
	model := s.modelForEvent(event)
	targets, err := decodeResolvedTargets(event.ResolvedTargets)
	if err != nil {
		return err
	}
	platforms := make([]string, 0, len(targets))
	for _, target := range targets {
		platforms = append(platforms, target.Platform)
	}
	runRequest := background.RunRequest{
		TargetPlatforms: platforms,
		NeedsElyphRules: event.Request.Format == elyph.Format,
		TaskType:        background.KindElnis,
		TaskID:          event.EventKey,
		Name:            firstNonEmpty(event.Request.Title, "Elnis: "+event.Request.Source),
		Actor:           elnisActor(event),
		ModelProvider:   model.Provider,
		Model:           model.Model,
		PromptSegments:  segments,
		Prompt:          s.llmPrompt(event),
		SandboxSubdir:   elnisSandboxSubdir(event.Request.Elwisp.Name),
		Metadata: map[string]string{
			"elnis_event_key": event.EventKey,
			"elwisp_name":     event.Request.Elwisp.Name,
			"elnis_source":    event.Request.Source,
			"elnis_source_id": event.Request.ID,
		},
	}
	runRequest.ToolListNames = event.Request.ToolListNames
	runRequest.ELwispTools = s.elwispTools(event)
	runRequest.AllowedToolNames = make([]string, 0)
	for name := range s.allowedInternalTools(event.Request.Elwisp.Name) {
		runRequest.AllowedToolNames = append(runRequest.AllowedToolNames, name)
	}

	ctx, result, err := s.runner.RunBackground(ctx, runRequest)
	identity.SessionID = result.SessionID
	if result.TakenOver {
		return s.completeTakeover(ctx, eventID, event.ResolvedTargets, result, err)
	}
	if err != nil {
		logFailure("run_model", err, slog.LevelError)
		return s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusFailed, result.SessionID, result.Text, err.Error())
	}
	if taken, lookupErr := s.sessionTakenOver(context.WithoutCancel(ctx), result.SessionID); lookupErr != nil {
		return lookupErr
	} else if taken {
		result.TakenOver = true
		return s.completeTakeover(ctx, eventID, event.ResolvedTargets, result, nil)
	}
	parsed, parseErr := background.ParseJSONResult(result.Text)
	if parseErr != nil {
		logFailure("parse_result", parseErr, slog.LevelWarn)
		ctx, result, err = s.retryLLMResultFormat(ctx, event, result.SessionID, model)
		if result.TakenOver {
			return s.completeTakeover(ctx, eventID, event.ResolvedTargets, result, err)
		}
		if err != nil {
			logFailure("run_model", err, slog.LevelError)
			return s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusFailed, result.SessionID, result.Text, err.Error())
		}
		parsed, parseErr = background.ParseJSONResult(result.Text)
	}
	if parseErr != nil {
		message := fmt.Sprintf("Elnis 事件 %s 解析格式失败，请查看后台 session。\nsession: %s\n错误：%v", event.EventKey, result.SessionID, parseErr)
		logFailure("task", parseErr, slog.LevelError)
		return s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusFailed, result.SessionID, message, parseErr.Error())
	}
	resultJSON, _ := json.Marshal(parsed)
	if parsed.NeedReport && (strings.TrimSpace(parsed.Report) != "" || len(parsed.ReportSegments) > 0) {
		prepared, err := s.prepareReport(ctx, event, eventID, string(resultJSON), result.SessionID, result.MessageID, parsed)
		if err != nil {
			logFailure("prepare_report", err, slog.LevelError)
			return s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusFailed, result.SessionID, string(resultJSON), err.Error())
		}
		if prepared {
			ctx, err = s.deliverReport(ctx, eventID)
			return err
		}
	} else if err := s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusCompleted, result.SessionID, string(resultJSON), ""); err != nil {
		return err
	}

	{
		content, _ := json.Marshal(map[string]any{"origin": event.Origin, "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "event_id": eventID})
		record := globalevents.LogRecord{
			Category:     globalevents.LogAudit,
			Level:        slog.LevelInfo,
			EventName:    "elnis.llm_completed",
			Module:       "elnis",
			Type:         globalevents.ContentJSON,
			Content:      string(content),
			ResultStatus: globalevents.ResultSucceeded,
		}
		record.Identity = identity
		record.Identity.SessionID = result.SessionID
		globalevents.Log(ctx, record)
		record.Category, record.EventName = globalevents.LogElnis, "elnis_llm_completed"
		globalevents.Log(ctx, record)
	}
	return nil
}

func (s *Service) retryLLMResultFormat(ctx context.Context, event Event, sessionID string, model config.ModelSelection) (context.Context, background.RunResult, error) {

	if taken, err := s.sessionTakenOver(ctx, sessionID); err != nil || taken {
		return ctx, background.RunResult{SessionID: sessionID, TakenOver: taken, Outcome: "taken_over"}, err
	}
	return s.runner.RunBackground(ctx, background.RunRequest{
		TaskType:      background.KindElnis,
		TaskID:        event.EventKey,
		Name:          firstNonEmpty(event.Request.Title, "Elnis: "+event.Request.Source),
		Actor:         elnisActor(event),
		SessionID:     sessionID,
		ModelProvider: model.Provider,
		Model:         model.Model,
		Prompt:        background.DefaultJSONRetryPrompt(),
		SandboxSubdir: elnisSandboxSubdir(event.Request.Elwisp.Name),
		Metadata:      map[string]string{"elnis_event_key": event.EventKey, "elwisp_name": event.Request.Elwisp.Name},
	})
}

func (s *Service) llmPrompt(event Event) string {
	parts := []string{"[系统 Elnis 后台事件任务]", ""}
	parts = append(parts, "** 只能调用本任务提供的工具；没有提供工具时，基于已有信息完成或报告阻塞")
	parts = append(parts,
		"** 按事件内容自主处理，当前无人值守",
		"** 信息不足时，在最终 JSON 的 report 填写失败或阻塞原因",
		"** 所有路径参数必须使用相对路径",
		"** 有投递目标、任务要求通知或产生需要目标知道的结果/失败/阻塞原因时，应设置 need_report=true 并在 report 写自然语言汇报",
		"** 最终回复必须是严格 JSON",
		"** JSON 格式：{\"completed\":true,\"need_report\":false,\"report\":\"\",\"report_segments\":[]}",
		"** report_segments 可选数组，元素为 {\"type\":\"image|file\",\"url\":\"相对路径\"} 或 {\"type\":\"image|file\",\"url\":\"media:<sha256>\"}，来源二选一。文件路径必须位于当前任务工作目录内",
		"** completed 表示是否完成任务",
		"** need_report 表示是否需要向目标平台汇报；成功、失败或阻塞都可以请求汇报",
		"** report 为需要发给目标平台的汇报，可填写处理结果、失败原因或阻塞原因",
		"~ 使用绝对路径、~、.. 或 cd。",
		"~ 闲聊",
		"~ 向用户提问",
		"~ 输出 Markdown 代码块",
		"~ 输出 JSON 外的任何文字",
		"",
		"事件标题："+strings.TrimSpace(event.Request.Title),
		"事件来源："+event.Request.Source,
		"事件 ID："+event.Request.ID,
		"事件格式："+event.Request.Format,
	)
	if len(event.Request.Meta) > 0 {
		if data, err := json.Marshal(event.Request.Meta); err == nil {
			parts = append(parts, "事件 metadata："+string(data))
		}
	}
	parts = append(parts, "", "事件内容：", event.Request.Content)
	return strings.Join(parts, "\n")
}

func (s *Service) modelForEvent(event Event) config.ModelSelection {
	slot := event.Request.ModelSlot
	if slot == "" {
		slot = "work"
	}
	selected := s.resolveModel(slot)
	if (selected.Provider == "" || selected.Model == "") && slot != "work" {
		selected = s.resolveModel("work")
	}
	return selected
}

func (s *Service) completeTakeover(ctx context.Context, eventID, resolved string, result background.RunResult, runErr error) error {
	payload, _ := json.Marshal(map[string]any{"taken_over": true, "run_id": result.RunID, "outcome": result.Outcome, "text": result.Text, "message_id": result.MessageID})
	message := ""
	if runErr != nil {
		message = runErr.Error()
	}
	if runErr != nil {
		globalevents.AsyncLog(ctx, globalevents.LogRecord{Category: globalevents.LogElnis, Level: slog.LevelInfo, EventName: "elnis_takeover_result", Module: "elnis", Content: runErr.Error(), Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.LogStatus(runErr)})
	}
	return s.completeEventWithSession(ctx, eventID, resolved, StatusTakenOver, result.SessionID, string(payload), message)
}
