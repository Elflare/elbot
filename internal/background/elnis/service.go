package elnis

import (
	"context"
	"elbot/internal/operationresult"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	background "elbot/internal/background/share"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/elvena"
	globalevents "elbot/internal/events"
	"elbot/internal/tool"
)

type SenderFunc func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (operationresult.Result[delivery.Receipt], error)

type QueuedLLMEvent struct {
	Event   Event
	EventID string
}

type EnqueueLLMFunc func(ctx context.Context, event QueuedLLMEvent) error

type ModelResolverFunc func(slot string) config.ModelSelection

type Options struct {
	Media       Media
	Config      config.ElnisConfig
	SandboxRoot string
	Tokens      map[string]string
	Store       EventRepository

	Send             SenderFunc
	Runner           background.Runner
	SessionTakenOver background.SessionTakenOver
	ResolveModel     ModelResolverFunc
	ToolPreloader    interface {
		BackgroundSelections(context.Context, []string) ([]tool.BackgroundSelection, error)
	} // 由 Tool 根部提供声明展开能力，授权仍归 Elnis。
	EnabledPlatforms []string
	CallPlatformAPI  func(ctx context.Context, platform, api string, params map[string]any) (json.RawMessage, error) // 由根部注入，Platform 负责按名称路由。
}

type Service struct {
	media       Media
	cfg         config.ElnisConfig
	sandboxRoot string
	tokens      map[string]string
	store       EventRepository

	send             SenderFunc
	runner           background.Runner
	sessionTakenOver background.SessionTakenOver
	resolveModel     ModelResolverFunc
	toolPreloader    interface {
		BackgroundSelections(context.Context, []string) ([]tool.BackgroundSelection, error)
	}
	enabledPlatforms []string
	callPlatformAPI  func(context.Context, string, string, map[string]any) (json.RawMessage, error)
	enqueueLLM       EnqueueLLMFunc
}

func NewService(opts Options) (*Service, error) {
	if opts.Config.Enabled && len(opts.Tokens) == 0 {
		return nil, fmt.Errorf("elnis enabled but no tokens are configured")
	}
	return &Service{
		media:            opts.Media,
		cfg:              opts.Config,
		sandboxRoot:      opts.SandboxRoot,
		tokens:           opts.Tokens,
		store:            opts.Store,
		send:             opts.Send,
		runner:           opts.Runner,
		sessionTakenOver: opts.SessionTakenOver,
		resolveModel:     opts.ResolveModel,
		toolPreloader:    opts.ToolPreloader,
		enabledPlatforms: uniqueSorted(opts.EnabledPlatforms),
		callPlatformAPI:  opts.CallPlatformAPI,
	}, nil
}

// RecoverInterrupted fails executions whose in-memory work cannot survive restart.
// Invoke before workers start, including when Elnis is disabled.
func RecoverInterrupted(ctx context.Context, repo EventRepository) error {
	return repo.FailInterrupted(ctx, []string{StatusAccepted, StatusQueued, StatusRunning},
		StatusFailed, "interrupted before durable report")
}

func (s *Service) SetLLMEnqueuer(enqueue EnqueueLLMFunc) {
	s.enqueueLLM = enqueue
}

func (s *Service) Handle(ctx context.Context, token string, req Request) (operationresult.Result[Response], error) {
	tokenName, ok := s.authenticate(token)
	if !ok {
		content, _ := json.Marshal(map[string]any{"error": "unauthorized"})
		record := globalevents.LogRecord{
			Category:     globalevents.LogCategoryAudit,
			Level:        slog.LevelWarn,
			EventName:    "elnis.auth_failed",
			Module:       "elnis",
			Type:         globalevents.ContentJSON,
			Content:      string(content),
			ResultStatus: globalevents.ResultRejected,
		}
		record.Identity = globalevents.LogIdentityFromContext(ctx)
		globalevents.Log(ctx, record)
		return operationresult.Result[Response]{Status: operationresult.Forbidden, Message: "unauthorized"}, nil
	}
	return s.DispatchElvena(ctx, elvena.Origin{Kind: elvena.OriginHTTPToken, Name: tokenName}, req)
}

func (s *Service) DispatchElvena(ctx context.Context, origin elvena.Origin, req Request) (result operationresult.Result[Response], err error) {
	defer func() {
		if err != nil {
			content, _ := json.Marshal(map[string]any{"origin": origin, "elwisp_name": req.Elwisp.Name, "source": req.Source, "source_id": req.ID, "mode": req.Mode, "error": err.Error()})
			record := globalevents.LogRecord{Category: globalevents.LogCategoryElnis, Level: slog.LevelError, EventName: "elnis_event_save_failed", Module: "elnis", Type: globalevents.ContentJSON, Content: string(content), Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.LogStatus(err)}
			if record.ResultStatus == globalevents.ResultCanceled {
				record.Level = slog.LevelInfo
			}
			globalevents.Log(ctx, record)
			return
		}
		if result.Status != operationresult.Success {
			content, _ := json.Marshal(map[string]any{"origin": origin, "elwisp_name": req.Elwisp.Name, "source": req.Source, "source_id": req.ID, "mode": req.Mode, "error": result.Message})
			record := globalevents.LogRecord{Category: globalevents.LogCategoryAudit, Level: slog.LevelWarn, EventName: "elnis.rejected", Module: "elnis", Type: globalevents.ContentJSON, Content: string(content), Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.ResultRejected}
			globalevents.Log(ctx, record)
		}
	}()
	if err := origin.Validate(); err != nil {
		return operationresult.Result[Response]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	prepared := s.prepareEvent(origin, req)
	if prepared.Status != operationresult.Success {
		return operationresult.Result[Response]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	return s.handlePreparedEvent(ctx, prepared.Data)
}

func (s *Service) handlePreparedEvent(ctx context.Context, event Event) (operationresult.Result[Response], error) {
	identity := globalevents.LogIdentityFromContext(ctx)

	req := event.Request

	if result := s.authorizeElwisp(event); result.Status != operationresult.Success {
		return operationresult.Result[Response]{Status: result.Status, Message: result.Message}, nil
	}
	result, err := s.authorizeInternalTools(ctx, event)
	if err != nil {
		return operationresult.Result[Response]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[Response]{Status: result.Status, Message: result.Message}, nil
	}
	if result := s.authorizeExternalTools(event); result.Status != operationresult.Success {
		return operationresult.Result[Response]{Status: result.Status, Message: result.Message}, nil
	}
	if existing, err := s.store.GetByKey(ctx, req.Elwisp.Name, req.Source, req.ID); err == nil {
		s.handleDuplicate(ctx, event, existing)
		return acceptedEventResult(event, StatusDuplicate, true, ""), nil
	} else if !errors.Is(err, ErrEventNotFound) {
		return operationresult.Result[Response]{}, err
	}

	status := StatusAccepted
	if req.Mode == ModeLLM {
		status = StatusQueued
	}
	var mediaIDs []string
	if req.Mode != ModeRecord {
		for _, seg := range event.Segments {
			if seg.Source.MediaID != "" {
				mediaIDs = append(mediaIDs, seg.Source.MediaID)
			}
		}
	}
	record, err := s.store.Create(ctx, CreateEventRequest{
		MediaIDs:   mediaIDs,
		EventKey:   event.EventKey,
		OriginKind: string(event.Origin.Kind), OriginName: event.Origin.Name,
		ElwispName:       req.Elwisp.Name,
		Source:           req.Source,
		SourceID:         req.ID,
		Tags:             event.TagsJSON,
		Mode:             req.Mode,
		ModelSlot:        req.ModelSlot,
		ContentHash:      event.ContentHash,
		ToolDeclarations: event.ToolDeclarations,
		ToolHash:         event.ToolHash,
		RequestedTargets: event.RequestedTargets,
		ResolvedTargets:  event.ResolvedTargets,
		Status:           status,
		ReceivedAt:       event.ReceivedAt,
		CreatedAt:        event.CreatedAt,
	})
	if err != nil {
		// 并发请求可能在预查之后创建同键事件；只在确认存在时按重复处理。
		if existing, lookupErr := s.store.GetByKey(ctx, req.Elwisp.Name, req.Source, req.ID); lookupErr == nil {
			s.handleDuplicate(ctx, event, existing)
			return acceptedEventResult(event, StatusDuplicate, true, ""), nil
		}
		return operationresult.Result[Response]{}, err
	}

	{
		content, _ := json.Marshal(map[string]any{"origin": event.Origin, "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "event_id": record.ID})
		record := globalevents.LogRecord{
			Category:  globalevents.LogCategoryAudit,
			Level:     slog.LevelInfo,
			EventName: "elnis.accepted",
			Module:    "elnis",
			Type:      globalevents.ContentJSON,
			Content:   string(content),
		}
		record.Identity = identity
		globalevents.Log(ctx, record)
		record.Category, record.EventName = globalevents.LogCategoryElnis, "elnis_event_accepted"
		globalevents.Log(ctx, record)
	}

	switch req.Mode {
	case ModeRecord:
		if err := s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusCompleted, "", ""); err != nil {
			return operationresult.Result[Response]{}, err
		}
		{
			content, _ := json.Marshal(map[string]any{"origin": event.Origin, "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "event_id": record.ID})
			record := globalevents.LogRecord{
				Category:     globalevents.LogCategoryAudit,
				Level:        slog.LevelInfo,
				EventName:    "elnis.recorded",
				Module:       "elnis",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ResultStatus: globalevents.ResultSucceeded,
			}
			record.Identity = identity
			globalevents.Log(ctx, record)
		}
		return acceptedEventResult(event, StatusCompleted, false, ""), nil
	case ModeDirect:
		result, err := s.runDirect(ctx, event, record.ID)
		if err != nil {
			logRecord := globalevents.LogRecord{Category: globalevents.LogCategoryElnis, Level: slog.LevelWarn, EventName: "elnis_direct_failed", Module: "elnis", Content: err.Error(), Identity: identity, ResultStatus: globalevents.LogStatus(err)}
			if logRecord.ResultStatus == globalevents.ResultCanceled {
				logRecord.Level = slog.LevelInfo
			}
			globalevents.Log(ctx, logRecord)
			persistenceErr := s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusFailed, "", err.Error())
			if persistenceErr != nil {
				return operationresult.Result[Response]{}, persistenceErr
			}
			return acceptedEventResult(event, StatusFailed, false, err.Error()), nil
		}
		if result.Status != operationresult.Success {
			globalevents.Log(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryElnis, Level: slog.LevelWarn, EventName: "elnis_direct_failed", Module: "elnis", Content: result.Message, Identity: identity, ResultStatus: globalevents.ResultFailed})
			persistenceErr := s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusFailed, "", result.Message)
			if persistenceErr != nil {
				return operationresult.Result[Response]{}, persistenceErr
			}
			return acceptedEventResult(event, StatusFailed, false, result.Message), nil
		}
		{
			content, _ := json.Marshal(map[string]any{"origin": event.Origin, "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "event_id": record.ID})
			record := globalevents.LogRecord{
				Category:     globalevents.LogCategoryAudit,
				Level:        slog.LevelInfo,
				EventName:    "elnis.direct_completed",
				Module:       "elnis",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ResultStatus: globalevents.ResultSucceeded,
			}
			record.Identity = identity
			globalevents.Log(ctx, record)
		}
		return acceptedEventResult(event, StatusCompleted, false, ""), nil
	case ModeLLM:
		{
			content, _ := json.Marshal(map[string]any{"origin": event.Origin, "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "event_id": record.ID})
			record := globalevents.LogRecord{
				Category:  globalevents.LogCategoryAudit,
				Level:     slog.LevelInfo,
				EventName: "elnis.llm_queued",
				Module:    "elnis",
				Type:      globalevents.ContentJSON,
				Content:   string(content),
			}
			record.Identity = identity
			globalevents.Log(ctx, record)
			record.Category, record.EventName = globalevents.LogCategoryElnis, "elnis_llm_queued"
			globalevents.Log(ctx, record)
		}
		if err := s.enqueueLLM(ctx, QueuedLLMEvent{Event: event, EventID: record.ID}); err != nil {
			{
				content := map[string]any{"origin": event.Origin, "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "event_id": record.ID, "error": fmt.Sprintf("%.2048s", err.Error())}
				data, _ := json.Marshal(content)
				record := globalevents.LogRecord{
					Category:     globalevents.LogCategoryElnis,
					Level:        slog.LevelError,
					EventName:    "elnis_llm_enqueue_failed",
					Module:       "elnis",
					Type:         globalevents.ContentJSON,
					Content:      string(data),
					ResultStatus: globalevents.ResultFailed,
				}
				if globalevents.LogStatus(err) == globalevents.ResultCanceled {
					record.ResultStatus = globalevents.ResultCanceled
					record.Level = slog.LevelInfo
				}
				record.Identity = identity
				globalevents.Log(ctx, record)
			}
			persistenceErr := s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusFailed, "", err.Error())
			if persistenceErr != nil {
				return operationresult.Result[Response]{}, persistenceErr
			}
			return acceptedEventResult(event, StatusFailed, false, err.Error()), nil
		}

		return acceptedEventResult(event, StatusQueued, false, ""), nil
	default:
		return operationresult.Result[Response]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("unsupported mode %q", req.Mode)}, nil
	}
}

func acceptedEventResult(event Event, status string, duplicate bool, message string) operationresult.Result[Response] {
	return operationresult.Result[Response]{Status: operationresult.Success, Data: Response{Accepted: true, Duplicate: duplicate, EventKey: event.EventKey, Mode: event.Request.Mode, Status: status, Error: message}}
}
