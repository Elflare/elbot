// 职责：调度自动命名并处理标题提交、失败重试及 fallback。
// 协调准备和生成流程，提交时尊重手动改名及取消状态。
package naming

import (
	"context"
	"encoding/json"
	"errors"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/storage"
)

// MaybeScheduleNaming 在任何 I/O 前占位；准备随触发调用取消，接纳后的任务归组件所有。
func (s *Service) MaybeScheduleNaming(ctx context.Context, id string) {
	worker, state := s.beginNaming(id)
	if state == nil {
		return
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.SessionID = id
	worker = contextinfo.WithExecution(worker, facts)
	if model, ok := contextinfo.ModelFromContext(ctx); ok {
		worker = contextinfo.WithModel(worker, model)
	}
	event := NamingCompletedEvent{Identity: events.LogIdentity{SessionID: &facts.SessionID}, TriggeredAt: storage.Now(), MaxFailures: maxNamingFailures}
	if facts.RunID != "" {
		event.Identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		event.Identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		event.Identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		event.Identity.RootRequestID = &facts.RootRequestID
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(worker, cancel)
	defer func() { stop(); cancel() }()
	scheduled := false
	defer func() {
		if !scheduled {
			if event.PreparationError != nil {
				s.completeNaming(worker, state, event)
			}
			s.finishNaming(id, state)
		}
	}()
	event.PreparationOperation = "load session"
	row, err := s.deps.Sessions.Get(ctx, id)
	if err == nil {
		title := row.Title
		event.Title = &title
		var fields storage.SessionMetadata
		fields, err = storage.DecodeSessionMetadata(row.Metadata)
		if err == nil {
			state.done, err = titleRenamed(fields)
		}
	}
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			s.ForgetSession(id)
		}
		event.PreparationError = err
		event.PreparationResultStatus = resultStatus(err)
		event.ResultStatus = event.PreparationResultStatus
		return
	}
	if state.done {
		return
	}
	event.PreparationOperation = "load messages"
	messages, err := s.deps.Messages.ListBySession(ctx, id)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		event.PreparationError = err
		event.PreparationResultStatus = resultStatus(err)
		event.ResultStatus = event.PreparationResultStatus
		return
	}
	messages = filterConversationMessages(messages)
	if len(messages) < s.config.TriggerStep {
		return
	}
	// 任务只保留选中的消息与 fallback 文本，避免慢模型持有整段会话历史。
	namingMessages := append([]storage.Message(nil), messages[:s.config.TriggerStep]...)
	fallback := fallbackTitle(messages)
	event.PreparationResultStatus = events.ResultSucceeded
	count := s.config.TriggerStep
	event.MessageCount = &count
	event.Scheduled = true
	s.notifyNamingScheduled(worker, NamingScheduledEvent{Identity: event.Identity, TriggeredAt: event.TriggeredAt, MessageCount: count, TriggerStep: count})
	scheduled = true
	go func() {
		defer s.finishNaming(id, state)
		s.runNaming(worker, id, state, namingMessages, fallback, event)
	}()
}

func (s *Service) runNaming(ctx context.Context, id string, state *namingState, messages []storage.Message, fallback string, event NamingCompletedEvent) {
	defer func() { s.completeNaming(ctx, state, event) }()
	if err := ctx.Err(); err != nil {
		event.CancellationError = err
		event.ResultStatus = resultStatus(err)
		return
	}
	if state.failures < maxNamingFailures {
		result, err := s.generateTitle(ctx, messages)
		event.Provider, event.Model, event.GeneratedTitleRaw = result.Provider, result.Model, result.RawTitle
		event.GenerationError = err
		event.GenerationResultStatus = events.ResultSucceeded
		if err != nil {
			event.GenerationResultStatus = events.ResultFailed
		}
		// 客户端可能忽略取消并交回成功；取消后不能写标题或增加失败数。
		if canceled := ctx.Err(); canceled != nil {
			if err != nil {
				event.GenerationResultStatus = events.ResultCanceled
			}
			event.CancellationError = canceled
			event.ResultStatus = resultStatus(canceled)
			return
		}
		if err == nil {
			event.GeneratedTitleNormalized = normalizeTitle(result.RawTitle)
			event.InvalidReason = invalidTitleReason(event.GeneratedTitleNormalized)
			valid := event.InvalidReason == ""
			event.TitleValid = &valid
			if valid {
				s.persistTitle(ctx, id, state, event.GeneratedTitleNormalized, false, &event)
				return
			}
		}
		state.failures++
	}
	event.ResultStatus = events.ResultFailed
	if state.failures < maxNamingFailures {
		return
	}
	event.FallbackTitle = fallback
	if invalidTitleReason(event.FallbackTitle) != "" {
		event.FallbackResultStatus = events.ResultSkipped
		return
	}
	s.persistTitle(ctx, id, state, event.FallbackTitle, true, &event)
}

func (s *Service) persistTitle(ctx context.Context, id string, state *namingState, title string, fallback bool, event *NamingCompletedEvent) {
	source := "auto"
	if fallback {
		source = "fallback"
	}
	latest, applied, err := s.saveGeneratedTitle(ctx, id, title, source)
	status := resultStatus(err)
	if err == nil {
		state.done = true
		actualTitle := latest.Title
		event.Title = &actualTitle
		if !applied {
			status = events.ResultSkipped
		}
	} else if errors.Is(err, storage.ErrNotFound) {
		s.ForgetSession(id)
	}
	if fallback {
		event.FallbackResultStatus, event.FallbackError = status, err
	} else {
		event.PersistenceResultStatus, event.PersistenceError = status, err
	}
	event.ResultStatus = status
}

// 最新保护标记与标题、完成事实在一个事务中处理，手动改名和后台标题优先。
func (s *Service) saveGeneratedTitle(ctx context.Context, id, title, source string) (*storage.Session, bool, error) {
	applied := false
	row, err := s.deps.Sessions.Mutate(ctx, id, func(row *storage.Session) error {
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		renamed, err := titleRenamed(fields)
		if err != nil || renamed {
			return err
		}
		fields["title_renamed"] = json.RawMessage(`true`)
		if err := fields.Set("title_source", source); err != nil {
			return err
		}
		metadata, err := fields.Encode()
		if err != nil {
			return err
		}
		row.Title, row.Metadata, row.UpdatedAt = title, metadata, storage.Now()
		applied = true
		return nil
	})
	return row, applied, err
}

func titleRenamed(fields storage.SessionMetadata) (bool, error) {
	var renamed bool
	if raw, ok := fields["title_renamed"]; ok {
		if err := json.Unmarshal(raw, &renamed); err != nil {
			return false, err
		}
	}
	return renamed, nil
}

func resultStatus(err error) events.ResultStatus {
	if err == nil {
		return events.ResultSucceeded
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return events.ResultCanceled
	}
	return events.ResultFailed
}

func (s *Service) completeNaming(ctx context.Context, state *namingState, event NamingCompletedEvent) {
	event.CompletedAt = storage.Now()
	event.FailureCount = state.failures
	s.notifyNamingCompleted(ctx, event)
}
