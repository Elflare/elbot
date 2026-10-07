package elnis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/elyph"
	globalevents "elbot/internal/events"
)

func (s *Service) RunLLMEvent(ctx context.Context, event Event, eventID string) error {
	attrs := s.eventAttrs(event)
	if s.runner == nil {
		err := fmt.Errorf("elnis background runner is not configured")
		_ = s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusFailed, "", err.Error())
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogElnis,
			Level:    failureLogLevel(err),
			Name:     "elnis_llm_failed",
			Module:   "elnis",
			Summary:  "elnis llm failed",
			Fields:   slog.Group("", append(attrs, "event_id", eventID, "error", err.Error())...).Value.Group(),
		})
		return err
	}
	if err := s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusRunning, "", ""); err != nil {
		return err
	}
	_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
		Category: globalevents.LogAudit,
		Level:    slog.LevelInfo,
		Name:     "elnis.llm_started",
		Module:   "elnis",
		Summary:  "elnis.llm_started",
		Fields:   slog.Group("", append(attrs, "event_id", eventID)...).Value.Group(),
	})
	_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
		Category: globalevents.LogElnis,
		Level:    slog.LevelInfo,
		Name:     "elnis_llm_started",
		Module:   "elnis",
		Summary:  "elnis llm started",
		Fields:   slog.Group("", append(attrs, "event_id", eventID)...).Value.Group(),
	})
	segments, err := s.materializeSegments(ctx, event.Request.Segments, eventID)
	if err != nil {
		_ = s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusFailed, "", err.Error())
		return err
	}
	model := s.modelForEvent(event)
	runRequest := background.RunRequest{
		Kind:           background.KindElnis,
		Name:           event.EventKey,
		Title:          firstNonEmpty(event.Request.Title, "Elnis: "+event.Request.Source),
		Platform:       firstPlatform(event.ResolvedTargets),
		Actor:          elnisActor(event),
		ScopeID:        "elnis:" + event.EventKey,
		ModelProvider:  model.Provider,
		Model:          model.Model,
		PromptSegments: segments,
		Prompt:         s.llmPrompt(event),
		SandboxSubdir:  elnisSandboxSubdir(event.Request.Elwisp.Name),
		Metadata: map[string]string{
			"elnis_event_key": event.EventKey,
			"elwisp_name":     event.Request.Elwisp.Name,
			"elnis_source":    event.Request.Source,
			"elnis_source_id": event.Request.ID,
		},
	}
	runRequest.ToolListNames = event.Request.ToolListNames
	runRequest.CachedTools = s.elwispCachedTools(event)
	runRequest.AllowedToolNames = make([]string, 0)
	for name := range s.allowedInternalTools(event.Request.Elwisp.Name) {
		runRequest.AllowedToolNames = append(runRequest.AllowedToolNames, name)
	}
	result, err := s.runner.RunBackground(ctx, runRequest)
	if taken, lookupErr := background.SessionTakenOver(context.WithoutCancel(ctx), s.store, result.SessionID); lookupErr != nil {
		return lookupErr
	} else if taken {
		result.TakenOver = true
	}
	if result.TakenOver {
		return s.completeTakeover(ctx, eventID, event.ResolvedTargets, result, err)
	}
	if err != nil {
		_ = s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusFailed, "", err.Error())
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogAudit,
			Level:    failureLogLevel(err),
			Name:     "elnis.llm_failed",
			Module:   "elnis",
			Summary:  "elnis.llm_failed",
			Fields:   slog.Group("", append(attrs, "event_id", eventID, "error", err.Error())...).Value.Group(),
		})
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogElnis,
			Level:    failureLogLevel(err),
			Name:     "elnis_llm_failed",
			Module:   "elnis",
			Summary:  "elnis llm failed",
			Fields:   slog.Group("", append(attrs, "event_id", eventID, "error", err.Error())...).Value.Group(),
		})
		return err
	}
	parsed, parseErr := background.ParseJSONResult(result.Text)
	if parseErr != nil {
		result, parsed, parseErr = s.retryLLMResultFormat(ctx, event, result.SessionID, model)
	}
	if result.TakenOver {
		return s.completeTakeover(ctx, eventID, event.ResolvedTargets, result, parseErr)
	}
	if parseErr != nil {
		message := fmt.Sprintf("Elnis 事件 %s 解析格式失败，请查看后台 session。\nsession: %s\n错误：%v", event.EventKey, result.SessionID, parseErr)
		_ = s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusFailed, result.SessionID, message, parseErr.Error())
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogAudit,
			Level:    failureLogLevel(parseErr),
			Name:     "elnis.llm_failed",
			Module:   "elnis",
			Summary:  "elnis.llm_failed",
			Fields:   slog.Group("", append(attrs, "event_id", eventID, "session_id", result.SessionID, "error", parseErr.Error())...).Value.Group(),
		})
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogElnis,
			Level:    failureLogLevel(parseErr),
			Name:     "elnis_llm_format_failed",
			Module:   "elnis",
			Summary:  "elnis llm format failed",
			Fields:   slog.Group("", append(attrs, "event_id", eventID, "session_id", result.SessionID, "error", parseErr.Error())...).Value.Group(),
		})
		return parseErr
	}
	resultJSON, _ := json.Marshal(parsed)
	if parsed.NeedReport && (strings.TrimSpace(parsed.Report) != "" || len(parsed.ReportSegments) > 0) {
		prepared, err := s.prepareReport(ctx, event, eventID, string(resultJSON), result.SessionID, result.MessageID, parsed)
		if err != nil {
			_ = s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusFailed, result.SessionID, string(resultJSON), err.Error())
			_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
				Category: globalevents.LogElnis,
				Level:    failureLogLevel(err),
				Name:     "elnis_llm_report_prepare_failed",
				Module:   "elnis",
				Summary:  "elnis llm report prepare failed",
				Fields:   slog.Group("", append(attrs, "event_id", eventID, "session_id", result.SessionID, "error", err.Error())...).Value.Group(),
			})
			return err
		}
		if prepared {
			if err := s.deliverReport(ctx, eventID); err != nil {
				_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
					Category: globalevents.LogElnis,
					Level:    slog.LevelWarn,
					Name:     "elnis_llm_report_delivery_failed",
					Module:   "elnis",
					Summary:  "elnis llm report delivery failed",
					Fields:   slog.Group("", append(attrs, "event_id", eventID, "session_id", result.SessionID, "error", err.Error())...).Value.Group(),
				})
				return err
			}
			return nil
		}
	} else if err := s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusCompleted, result.SessionID, string(resultJSON), ""); err != nil {
		return err
	}
	_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
		Category: globalevents.LogAudit,
		Level:    slog.LevelInfo,
		Name:     "elnis.llm_completed",
		Module:   "elnis",
		Summary:  "elnis.llm_completed",
		Fields:   slog.Group("", append(attrs, "event_id", eventID, "session_id", result.SessionID)...).Value.Group(),
	})
	_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
		Category: globalevents.LogElnis,
		Level:    slog.LevelInfo,
		Name:     "elnis_llm_completed",
		Module:   "elnis",
		Summary:  "elnis llm completed",
		Fields:   slog.Group("", append(attrs, "event_id", eventID, "session_id", result.SessionID)...).Value.Group(),
	})
	return nil
}

func (s *Service) retryLLMResultFormat(ctx context.Context, event Event, sessionID string, model config.ModelSelection) (background.RunResult, background.JSONResult, error) {
	if taken, err := background.SessionTakenOver(ctx, s.store, sessionID); err != nil || taken {
		return background.RunResult{SessionID: sessionID, TakenOver: taken, Outcome: "taken_over"}, background.JSONResult{}, err
	}
	result, err := s.runner.RunBackground(ctx, background.RunRequest{
		Kind:          background.KindElnis,
		Name:          event.EventKey,
		Title:         firstNonEmpty(event.Request.Title, "Elnis: "+event.Request.Source),
		Platform:      firstPlatform(event.ResolvedTargets),
		Actor:         elnisActor(event),
		ScopeID:       "elnis:" + event.EventKey,
		SessionID:     sessionID,
		ModelProvider: model.Provider,
		Model:         model.Model,
		Prompt:        background.DefaultJSONRetryPrompt(),
		SandboxSubdir: elnisSandboxSubdir(event.Request.Elwisp.Name),
		Metadata:      map[string]string{"elnis_event_key": event.EventKey, "elwisp_name": event.Request.Elwisp.Name},
	})
	if err != nil {
		return result, background.JSONResult{}, err
	}
	if result.TakenOver {
		return result, background.JSONResult{}, nil
	}
	parsed, err := background.ParseJSONResult(result.Text)
	return result, parsed, err
}

func (s *Service) llmPrompt(event Event) string {
	format := strings.TrimSpace(event.Request.Format)
	if format == "" {
		format = "text"
	}
	parts := []string{"[系统 Elnis 后台事件任务]", ""}
	if format == "elyph" {
		parts = append(parts, elyph.RuleCard(), "")
	}
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
		"事件格式："+format,
	)
	if len(event.Request.Meta) > 0 {
		if data, err := json.Marshal(event.Request.Meta); err == nil {
			parts = append(parts, "事件 metadata："+string(data))
		}
	}
	parts = append(parts, "", "事件内容：", strings.TrimSpace(event.Request.Content))
	return strings.Join(parts, "\n")
}

func (s *Service) modelForEvent(event Event) config.ModelSelection {
	slot := strings.TrimSpace(event.Request.ModelSlot)
	if slot == "" {
		slot = "work"
	}
	if s.resolveModel == nil {
		return config.ModelSelection{}
	}
	selected := s.resolveModel(slot)
	if (selected.Provider == "" || selected.Model == "") && slot != "work" {
		selected = s.resolveModel("work")
	}
	return selected
}

func (s *Service) completeTakeover(ctx context.Context, eventID, resolved string, result background.RunResult, runErr error) error {
	payload, err := json.Marshal(map[string]any{"taken_over": true, "run_id": result.RunID, "outcome": result.Outcome, "text": result.Text, "message_id": result.MessageID})
	if err != nil {
		return err
	}
	message := ""
	if runErr != nil {
		message = runErr.Error()
	}
	if err := s.completeEventWithSession(context.WithoutCancel(ctx), eventID, resolved, StatusTakenOver, result.SessionID, string(payload), message); err != nil {
		return err
	}
	return runErr
}
