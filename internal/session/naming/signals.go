// 定义命名过程事实并组织完整日志；发布能力由 Session 根部注入。
package naming

import (
	"context"
	"elbot/internal/logging"
	"encoding/json"
	"log/slog"
	"time"

	"elbot/internal/events"
)

type NamingScheduledEvent struct {
	Identity     events.LogIdentity // 来源固定的关联快照，指针只读，nil 表示缺失。
	TriggeredAt  time.Time          // 开始准备的时刻。
	MessageCount int                // 选中的消息数。
	TriggerStep  int                // 本次配置阈值。
}

type NamingCompletedEvent struct {
	Identity                 events.LogIdentity  // 来源固定的关联快照，不由订阅者补全。
	TriggeredAt              time.Time           // 开始准备的时刻。
	CompletedAt              time.Time           // 业务结束、终态通知之前的时刻。
	Scheduled                bool                // 是否发布过 Scheduled。
	MessageCount             *int                // nil 表示准备未确定消息数。
	ResultStatus             events.ResultStatus // 取得可用标题的最终结果。
	PreparationOperation     string              // 最后执行的准备操作。
	PreparationResultStatus  events.ResultStatus // 零值表示未执行。
	PreparationError         error               // 原始准备错误。
	GenerationResultStatus   events.ResultStatus // 零值表示未调用模型。
	GenerationError          error               // 保留生成错误，不因文本 fallback 成功丢弃。
	Provider                 string              // 最后调用的 provider，空表示未知。
	Model                    string              // 最后调用的 model，空表示未知。
	GeneratedTitleRaw        string              // 包括失败时的部分输出。
	GeneratedTitleNormalized string              // 规范化结果，不意味着已经保存。
	TitleValid               *bool               // nil 表示未验证。
	InvalidReason            string              // 无效原因。
	PersistenceResultStatus  events.ResultStatus // 生成标题的保存结果，零值表示未执行。
	PersistenceError         error               // 原始保存错误。
	FallbackTitle            string              // 选中的文本 fallback，空表示没有候选。
	FallbackResultStatus     events.ResultStatus // 文本 fallback 保存结果。
	FallbackError            error               // 独立保留 fallback 保存错误。
	Title                    *string             // 实际读到或保存的标题，nil 表示未知。
	CancellationError        error               // 操作之间观察到的取消。
	FailureCount             int                 // 本次缓存生命期内的生成失败数。
	MaxFailures              int                 // 生成失败上限。
}

func (s *Service) notifyNamingScheduled(ctx context.Context, event NamingScheduledEvent) {
	// 日志投递拥有自己的诊断，不把它变成 Naming 观察回调的失败。
	content, _ := json.Marshal(map[string]any{"message_count": event.MessageCount, "trigger_step": event.TriggerStep})
	s.deps.Log(ctx, events.LogRecord{
		Category: events.LogCategoryLog, Level: slog.LevelInfo, EventName: "session_naming_scheduled", Module: "session", Identity: event.Identity,
		Type: events.ContentJSON, Content: string(content),
	})
}

func (s *Service) notifyNamingCompleted(ctx context.Context, event NamingCompletedEvent) {
	level := slog.LevelInfo
	if event.PreparationResultStatus == events.ResultFailed || event.PersistenceResultStatus == events.ResultFailed || event.FallbackResultStatus == events.ResultFailed {
		level = slog.LevelError
	} else if event.ResultStatus == events.ResultFailed || event.GenerationResultStatus == events.ResultFailed || event.TitleValid != nil && !*event.TitleValid {
		level = slog.LevelWarn
	}
	content := map[string]any{
		"provider": event.Provider, "model": event.Model,
		"failure_count": event.FailureCount, "max_failures": event.MaxFailures,
	}
	for _, stage := range []struct {
		name   string
		status events.ResultStatus
		err    error
		extra  map[string]any
	}{
		{"preparation", event.PreparationResultStatus, event.PreparationError, map[string]any{"operation": event.PreparationOperation}},
		{"generation", event.GenerationResultStatus, event.GenerationError, nil},
		{"persistence", event.PersistenceResultStatus, event.PersistenceError, nil},
		{"fallback", event.FallbackResultStatus, event.FallbackError, map[string]any{"title": logging.TruncateContent(event.FallbackTitle, 4096)}},
	} {
		if stage.status == events.ResultUnspecified {
			continue
		}
		detail := map[string]any{"result_status": stage.status.String()}
		for key, value := range stage.extra {
			detail[key] = value
		}
		if stage.err != nil {
			detail["error"] = logging.TruncateContent(stage.err.Error(), 4096)
		}
		content[stage.name] = detail
	}
	if event.TitleValid != nil && !*event.TitleValid {
		content["invalid_title"] = logging.TruncateContent(event.InvalidReason, 4096)
	}
	if event.CancellationError != nil {
		content["cancellation"] = logging.TruncateContent(event.CancellationError.Error(), 4096)
	}
	if event.Title != nil {
		content["title"] = logging.TruncateContent(*event.Title, 4096)
	}
	if event.MessageCount != nil {
		content["message_count"] = *event.MessageCount
	}
	data, _ := json.Marshal(content)
	record := events.LogRecord{Category: events.LogCategoryLog, Level: level, EventName: "session_naming_completed", Module: "session", Type: events.ContentJSON, Content: string(data), Identity: event.Identity, ResultStatus: event.ResultStatus}
	s.deps.Log(ctx, record)
	if event.GenerationResultStatus == events.ResultFailed {
		record.Category = events.LogCategoryAudit
		s.deps.Log(ctx, record)
	}
}
