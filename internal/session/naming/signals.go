// 职责：定义命名过程事件并完成日志投影。
// 持有命名信号与日志订阅，随命名组件统一释放。
package naming

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	_ = events.EmitLog(ctx, events.LogRecord{
		Category: events.LogRuntime, Level: slog.LevelInfo, EventName: "session_naming_scheduled", Module: "session", Identity: event.Identity,
		Content: fmt.Sprintf("session naming scheduled: message_count=%d trigger_step=%d", event.MessageCount, event.TriggerStep),
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
	var summary strings.Builder
	fmt.Fprintf(&summary, "session naming completed: result=%s provider=%s model=%s", event.ResultStatus, event.Provider, event.Model)
	if event.PreparationResultStatus != events.ResultUnspecified {
		fmt.Fprintf(&summary, "\npreparation %s: %s; error=%v", event.PreparationOperation, event.PreparationResultStatus, event.PreparationError)
	}
	if event.GenerationResultStatus != events.ResultUnspecified {
		fmt.Fprintf(&summary, "\ngeneration: %s; error=%v", event.GenerationResultStatus, event.GenerationError)
	}
	if event.TitleValid != nil && !*event.TitleValid {
		fmt.Fprintf(&summary, "\ninvalid title: %s", event.InvalidReason)
	}
	if event.PersistenceResultStatus != events.ResultUnspecified {
		fmt.Fprintf(&summary, "\npersistence: %s; error=%v", event.PersistenceResultStatus, event.PersistenceError)
	}
	if event.FallbackResultStatus != events.ResultUnspecified {
		fmt.Fprintf(&summary, "\nfallback: %s title=%q; error=%v", event.FallbackResultStatus, event.FallbackTitle, event.FallbackError)
	}
	if event.CancellationError != nil {
		fmt.Fprintf(&summary, "\ncancellation: %v", event.CancellationError)
	}
	if event.Title != nil {
		fmt.Fprintf(&summary, "\nactual title=%q", *event.Title)
	}
	if event.MessageCount != nil {
		fmt.Fprintf(&summary, "\nmessage_count=%d", *event.MessageCount)
	}
	fmt.Fprintf(&summary, " failures=%d/%d", event.FailureCount, event.MaxFailures)
	record := events.LogRecord{Category: events.LogRuntime, Level: level, EventName: "session_naming_completed", Module: "session", Content: summary.String(), ContentLimit: 4096, Identity: event.Identity, ResultStatus: event.ResultStatus}
	_ = events.EmitLog(ctx, record)
	if event.GenerationResultStatus == events.ResultFailed {
		record.Category = events.LogAudit
		_ = events.EmitLog(ctx, record)
	}
	return nil
}
