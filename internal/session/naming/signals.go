// 职责：定义命名过程事件并完成日志投影。
// 持有命名信号与日志订阅，随命名组件统一释放。
package naming

import (
	"context"
	"elbot/internal/logging"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"elbot/internal/events"
	"elbot/internal/signal"
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

type namingSignals struct {
	Scheduled *signal.Signal[NamingScheduledEvent, signal.Empty]
	Completed *signal.Signal[NamingCompletedEvent, signal.Empty]
}

func newNamingSignals() namingSignals {
	return namingSignals{Scheduled: signal.New[NamingScheduledEvent, signal.Empty]("session.naming_scheduled"), Completed: signal.New[NamingCompletedEvent, signal.Empty]("session.naming_completed")}
}

func (s *Service) connectLogSignals() error {
	logs := namingLogProjection{}
	connection, err := s.namingSignals.Scheduled.Observe(logs.scheduled, signal.ConnectOptions{})
	if err != nil {
		return err
	}
	s.logConnections = append(s.logConnections, connection)
	connection, err = s.namingSignals.Completed.Observe(logs.completed, signal.ConnectOptions{})
	if err != nil {
		return err
	}
	s.logConnections = append(s.logConnections, connection)
	return nil
}

func (s *Service) notifyNamingScheduled(ctx context.Context, event NamingScheduledEvent) {
	event.Identity = copyIdentity(event.Identity)
	if _, err := s.namingSignals.Scheduled.Emit(context.WithoutCancel(ctx), event); err != nil {
		reportNamingDeliveryFailure(ctx, "session naming scheduled delivery failed", event.Identity, err)
	}
}

func (s *Service) notifyNamingCompleted(ctx context.Context, event NamingCompletedEvent) {
	event.Identity = copyIdentity(event.Identity)
	if event.Title != nil {
		value := *event.Title
		event.Title = &value
	}
	if event.MessageCount != nil {
		value := *event.MessageCount
		event.MessageCount = &value
	}
	if event.TitleValid != nil {
		value := *event.TitleValid
		event.TitleValid = &value
	}
	if _, err := s.namingSignals.Completed.Emit(context.WithoutCancel(ctx), event); err != nil {
		reportNamingDeliveryFailure(ctx, "session naming completed delivery failed", event.Identity, err)
	}
}

// 普通回调错误由 signal 诊断；本层只补充其不报告的投递状态。
// 只汇总这些状态，避免把已报告的普通错误再次带入摘要。
func reportNamingDeliveryFailure(ctx context.Context, message string, identity events.LogIdentity, err error) {
	var failures []error
	for _, failure := range []error{signal.ErrQueueFull, signal.ErrClosed, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, failure) {
			failures = append(failures, failure)
		}
	}
	if len(failures) == 0 {
		return
	}
	attrs := []slog.Attr{slog.Any("error", errors.Join(failures...))}
	for _, field := range []struct {
		key   string
		value *string
	}{
		{"session_id", identity.SessionID},
		{"request_id", identity.RequestID},
		{"run_id", identity.RunID},
		{"attempt", identity.Attempt},
		{"root_request_id", identity.RootRequestID},
	} {
		if field.value != nil {
			attrs = append(attrs, slog.String(field.key, *field.value))
		}
	}
	slog.LogAttrs(ctx, slog.LevelWarn, message, attrs...)
}

func copyIdentity(identity events.LogIdentity) events.LogIdentity {
	for _, field := range []**string{&identity.SessionID, &identity.RunID, &identity.Attempt, &identity.RequestID, &identity.RootRequestID} {
		if *field != nil {
			value := **field
			*field = &value
		}
	}
	return identity
}

type namingLogProjection struct{}

func (n namingLogProjection) scheduled(ctx context.Context, event NamingScheduledEvent) error {
	// 日志投递拥有自己的诊断，不把它变成 Naming 观察回调的失败。
	content, _ := json.Marshal(map[string]any{"message_count": event.MessageCount, "trigger_step": event.TriggerStep})
	_ = events.EmitLog(ctx, events.LogRecord{
		Category: events.LogRuntime, Level: slog.LevelInfo, EventName: "session_naming_scheduled", Module: "session", Identity: event.Identity,
		Type: events.ContentJSON, Content: string(content),
	})
	return nil
}

func (n namingLogProjection) completed(ctx context.Context, event NamingCompletedEvent) error {
	level := slog.LevelInfo
	if event.ResultStatus == events.ResultFailed {
		level = slog.LevelError
	} else if event.GenerationResultStatus == events.ResultFailed || event.TitleValid != nil && !*event.TitleValid {
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
	record := events.LogRecord{Category: events.LogRuntime, Level: level, EventName: "session_naming_completed", Module: "session", Type: events.ContentJSON, Content: string(data), Identity: event.Identity, ResultStatus: event.ResultStatus}
	_ = events.EmitLog(ctx, record)
	if event.GenerationResultStatus == events.ResultFailed {
		record.Category = events.LogAudit
		_ = events.EmitLog(ctx, record)
	}
	return nil
}
