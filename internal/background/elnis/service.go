package elnis

import (
	"context"
	"elbot/internal/contextinfo"
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

type SenderFunc func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error)

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

func (s *Service) Handle(ctx context.Context, token string, req Request) (Response, error) {
	tokenName, ok := s.authenticate(token)
	if !ok {
		{
			content, _ := json.Marshal(map[string]any{"error": "unauthorized"})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelWarn,
				EventName:    "elnis.auth_failed",
				Module:       "elnis",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ResultStatus: globalevents.ResultRejected,
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
		return Response{Accepted: false, Status: StatusFailed, Error: "unauthorized"}, fmt.Errorf("unauthorized")
	}
	return s.DispatchElvena(ctx, elvena.Origin{Kind: elvena.OriginHTTPToken, Name: tokenName}, req)
}

func (s *Service) DispatchElvena(ctx context.Context, origin elvena.Origin, req Request) (Response, error) {
	if err := origin.Validate(); err != nil {
		return Response{Accepted: false, Status: StatusFailed, Error: err.Error()}, err
	}
	event, err := s.prepareEvent(origin, req)
	if err != nil {
		{
			content, _ := json.Marshal(map[string]any{"origin": origin.Label(), "error": fmt.Sprintf("%.2048s", err.Error())})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelWarn,
				EventName:    "elnis.rejected",
				Module:       "elnis",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ResultStatus: globalevents.ResultRejected,
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
		return Response{Accepted: false, Status: StatusFailed, Error: err.Error()}, err
	}
	return s.handlePreparedEvent(ctx, event)
}

func (s *Service) handlePreparedEvent(ctx context.Context, event Event) (Response, error) {
	identity := globalevents.LogIdentity{}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if facts.SessionID != "" {
		identity.SessionID = &facts.SessionID
	}
	if facts.RunID != "" {
		identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		identity.RootRequestID = &facts.RootRequestID
	}

	req := event.Request

	if err := s.authorizeElwisp(event); err != nil {
		{
			content, _ := json.Marshal(map[string]any{"origin": event.Origin.Label(), "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "error": fmt.Sprintf("%.2048s", err.Error())})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelWarn,
				EventName:    "elnis.permission_denied",
				Module:       "elnis",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ResultStatus: globalevents.ResultRejected,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
			record.Category, record.EventName = globalevents.LogElnis, "elnis_permission_denied"
			background.LogBackground(ctx, record)
		}
		return Response{Accepted: false, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
	}
	if err := s.authorizeInternalTools(ctx, event); err != nil {
		{
			content, _ := json.Marshal(map[string]any{"origin": event.Origin.Label(), "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "error": fmt.Sprintf("%.2048s", err.Error())})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelWarn,
				EventName:    "elnis.tool_denied",
				Module:       "elnis",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ResultStatus: globalevents.ResultRejected,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
			record.Category, record.EventName = globalevents.LogElnis, "elnis_internal_tool_denied"
			background.LogBackground(ctx, record)
		}
		return Response{Accepted: false, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
	}
	if err := s.authorizeExternalTools(event); err != nil {
		{
			content, _ := json.Marshal(map[string]any{"origin": event.Origin.Label(), "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "error": fmt.Sprintf("%.2048s", err.Error())})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelWarn,
				EventName:    "elnis.external_tool_denied",
				Module:       "elnis",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ResultStatus: globalevents.ResultRejected,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
			record.Category, record.EventName = globalevents.LogElnis, "elnis_external_tool_denied"
			background.LogBackground(ctx, record)
		}
		return Response{Accepted: false, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
	}
	if existing, err := s.store.GetByKey(ctx, req.Elwisp.Name, req.Source, req.ID); err == nil {
		s.handleDuplicate(ctx, event, existing)
		return Response{Accepted: true, Duplicate: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusDuplicate}, nil
	} else if !errors.Is(err, ErrEventNotFound) {
		return Response{}, err
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
		MediaIDs:         mediaIDs,
		EventKey:         event.EventKey,
		TokenName:        event.Origin.Label(),
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
		return Response{}, err
	}

	{
		content, _ := json.Marshal(map[string]any{"origin": event.Origin.Label(), "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "event_id": record.ID})
		record := globalevents.LogRecord{
			Category:  globalevents.LogAudit,
			Level:     slog.LevelInfo,
			EventName: "elnis.accepted",
			Module:    "elnis",
			Type:      globalevents.ContentJSON,
			Content:   string(content),
		}
		record.Identity = identity
		background.LogBackground(ctx, record)
		record.Category, record.EventName = globalevents.LogElnis, "elnis_event_accepted"
		background.LogBackground(ctx, record)
	}

	switch req.Mode {
	case ModeRecord:
		if err := s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusCompleted, "", ""); err != nil {
			return Response{}, err
		}
		{
			content, _ := json.Marshal(map[string]any{"origin": event.Origin.Label(), "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "event_id": record.ID})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelInfo,
				EventName:    "elnis.recorded",
				Module:       "elnis",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ResultStatus: globalevents.ResultSucceeded,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
		return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusCompleted}, nil
	case ModeDirect:
		logResult, err := s.runDirect(ctx, event, record.ID)
		if err != nil {
			persistenceErr := s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusFailed, "", err.Error())
			{
				content := map[string]any{"origin": event.Origin.Label(), "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "event_id": record.ID, "error": fmt.Sprintf("%.2048s", err.Error()), "result": fmt.Sprintf("%.2048s", logResult.Content)}
				if persistenceErr != nil {
					content["persistence_error"] = fmt.Sprintf("%.2048s", persistenceErr.Error())
				}
				data, _ := json.Marshal(content)
				record := globalevents.LogRecord{
					Category:     globalevents.LogAudit,
					Level:        slog.LevelError,
					EventName:    "elnis.direct_failed",
					Module:       "elnis",
					Type:         globalevents.ContentJSON,
					Content:      string(data),
					ResultStatus: logResult.ResultStatus,
				}
				if logResult.ResultStatus != globalevents.ResultFailed {
					record.Level = slog.LevelInfo
				}
				record.Identity = identity
				if persistenceErr != nil {
					if record.ResultStatus != globalevents.ResultFailed {
						record.ResultStatus = globalevents.ResultFailed
						if errors.Is(persistenceErr, context.Canceled) {
							record.ResultStatus = globalevents.ResultCanceled
						}
					}
					if record.ResultStatus == globalevents.ResultFailed {
						record.Level = slog.LevelError
					}
				}
				background.LogBackground(ctx, record)
				record.Category, record.EventName = globalevents.LogElnis, "elnis_direct_failed"
				background.LogBackground(ctx, record)
			}
			if persistenceErr != nil {
				err = errors.Join(err, persistenceErr)
			}
			return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
		}
		{
			content, _ := json.Marshal(map[string]any{"origin": event.Origin.Label(), "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "event_id": record.ID, "result": fmt.Sprintf("%.2048s", logResult.Content)})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelInfo,
				EventName:    "elnis.direct_completed",
				Module:       "elnis",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ResultStatus: logResult.ResultStatus,
			}
			if logResult.ResultStatus == globalevents.ResultFailed {
				record.Level = slog.LevelError
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
		return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusCompleted}, nil
	case ModeLLM:
		{
			content, _ := json.Marshal(map[string]any{"origin": event.Origin.Label(), "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "event_id": record.ID})
			record := globalevents.LogRecord{
				Category:  globalevents.LogAudit,
				Level:     slog.LevelInfo,
				EventName: "elnis.llm_queued",
				Module:    "elnis",
				Type:      globalevents.ContentJSON,
				Content:   string(content),
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
			record.Category, record.EventName = globalevents.LogElnis, "elnis_llm_queued"
			background.LogBackground(ctx, record)
		}
		if err := s.enqueueLLM(ctx, QueuedLLMEvent{Event: event, EventID: record.ID}); err != nil {
			persistenceErr := s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusFailed, "", err.Error())
			{
				content := map[string]any{"origin": event.Origin.Label(), "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "event_id": record.ID, "error": fmt.Sprintf("%.2048s", err.Error())}
				if persistenceErr != nil {
					content["persistence_error"] = fmt.Sprintf("%.2048s", persistenceErr.Error())
				}
				data, _ := json.Marshal(content)
				record := globalevents.LogRecord{
					Category:     globalevents.LogElnis,
					Level:        slog.LevelError,
					EventName:    "elnis_llm_enqueue_failed",
					Module:       "elnis",
					Type:         globalevents.ContentJSON,
					Content:      string(data),
					ResultStatus: globalevents.ResultFailed,
				}
				if errors.Is(err, context.Canceled) {
					record.ResultStatus = globalevents.ResultCanceled
					record.Level = slog.LevelInfo
				}
				record.Identity = identity
				if persistenceErr != nil {
					record.ResultStatus = globalevents.ResultFailed
					record.Level = slog.LevelError
				}
				background.LogBackground(ctx, record)
			}
			if persistenceErr != nil {
				err = errors.Join(err, persistenceErr)
			}
			return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
		}

		return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusQueued}, nil
	default:
		return Response{}, fmt.Errorf("unsupported mode %q", req.Mode)
	}
}
