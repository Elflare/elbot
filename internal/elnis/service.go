package elnis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/elvena"
	globalevents "elbot/internal/events"
	"elbot/internal/media"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
)

type SenderFunc func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error)

type QueuedLLMEvent struct {
	Event   Event
	EventID string
}

type EnqueueLLMFunc func(ctx context.Context, event QueuedLLMEvent) error

type ModelResolverFunc func(slot string) config.ModelSelection

type Options struct {
	Media       *media.Manager
	Config      config.ElnisConfig
	SandboxRoot string
	Tokens      map[string]string
	Store       storage.Store

	Send             SenderFunc
	Runner           background.Runner
	ResolveModel     ModelResolverFunc
	ToolPreloader    *toolrun.PreloadService
	EnabledPlatforms []string
	PlatformCallers  elvena.PlatformCallerResolver
}

type Service struct {
	media       *media.Manager
	cfg         config.ElnisConfig
	sandboxRoot string
	tokens      map[string]string
	store       storage.Store

	send             SenderFunc
	runner           background.Runner
	resolveModel     ModelResolverFunc
	toolPreloader    *toolrun.PreloadService
	enabledPlatforms []string
	platformCallers  elvena.PlatformCallerResolver
	enqueueLLM       EnqueueLLMFunc
}

func NewService(opts Options) (*Service, error) {
	if opts.SandboxRoot == "" {
		opts.SandboxRoot = filepath.Join("data", "sandbox")
	}
	if opts.Store == nil || opts.Store.ElnisEvents() == nil {
		return nil, fmt.Errorf("elnis event store is not configured")
	}
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
		resolveModel:     opts.ResolveModel,
		toolPreloader:    opts.ToolPreloader,
		enabledPlatforms: uniqueSorted(opts.EnabledPlatforms),
		platformCallers:  opts.PlatformCallers,
	}, nil
}

// RecoverInterrupted fails executions whose in-memory work cannot survive restart.
// The app calls it before workers start, including when Elnis is disabled.
func RecoverInterrupted(ctx context.Context, repo storage.ElnisEventRepository) error {
	return repo.FailInterrupted(ctx, []string{StatusAccepted, StatusQueued, StatusRunning},
		StatusFailed, "interrupted before durable report")
}

func (s *Service) SetLLMEnqueuer(enqueue EnqueueLLMFunc) {
	s.enqueueLLM = enqueue
}

func (s *Service) Handle(ctx context.Context, token string, req Request) (Response, error) {
	tokenName, ok := s.authenticate(token)
	if !ok {
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{Category: globalevents.LogAudit, Level: slog.LevelWarn, Name: "elnis.auth_failed", Module: "elnis", Summary: "elnis.auth_failed", Fields: nil})
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
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogAudit,
			Level:    slog.LevelWarn,
			Name:     "elnis.rejected",
			Module:   "elnis",
			Summary:  "elnis.rejected",
			Fields:   []slog.Attr{slog.Any("origin", origin.Label()), slog.Any("error", err.Error())},
		})
		return Response{Accepted: false, Status: StatusFailed, Error: err.Error()}, err
	}
	return s.handlePreparedEvent(ctx, event)
}

func (s *Service) handlePreparedEvent(ctx context.Context, event Event) (Response, error) {
	req := event.Request
	attrs := s.eventAttrs(event)
	if err := s.authorizeElwisp(event); err != nil {
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogAudit,
			Level:    slog.LevelWarn,
			Name:     "elnis.permission_denied",
			Module:   "elnis",
			Summary:  "elnis.permission_denied",
			Fields:   slog.Group("", append(attrs, "error", err.Error())...).Value.Group(),
		})
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogElnis,
			Level:    slog.LevelWarn,
			Name:     "elnis_permission_denied",
			Module:   "elnis",
			Summary:  "elnis permission denied",
			Fields:   slog.Group("", append(attrs, "error", err.Error())...).Value.Group(),
		})
		return Response{Accepted: false, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
	}
	if err := s.authorizeInternalTools(ctx, event); err != nil {
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogAudit,
			Level:    slog.LevelWarn,
			Name:     "elnis.tool_denied",
			Module:   "elnis",
			Summary:  "elnis.tool_denied",
			Fields:   slog.Group("", append(attrs, "error", err.Error())...).Value.Group(),
		})
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogElnis,
			Level:    slog.LevelWarn,
			Name:     "elnis_internal_tool_denied",
			Module:   "elnis",
			Summary:  "elnis internal tool denied",
			Fields:   slog.Group("", append(attrs, "error", err.Error())...).Value.Group(),
		})
		return Response{Accepted: false, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
	}
	if err := s.authorizeExternalTools(event); err != nil {
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogAudit,
			Level:    slog.LevelWarn,
			Name:     "elnis.external_tool_denied",
			Module:   "elnis",
			Summary:  "elnis.external_tool_denied",
			Fields:   slog.Group("", append(attrs, "error", err.Error())...).Value.Group(),
		})
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogElnis,
			Level:    slog.LevelWarn,
			Name:     "elnis_external_tool_denied",
			Module:   "elnis",
			Summary:  "elnis external tool denied",
			Fields:   slog.Group("", append(attrs, "error", err.Error())...).Value.Group(),
		})
		return Response{Accepted: false, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
	}
	if existing, err := s.store.ElnisEvents().GetByKey(ctx, req.Elwisp.Name, req.Source, req.ID); err == nil {
		s.handleDuplicate(ctx, event, existing)
		return Response{Accepted: true, Duplicate: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusDuplicate}, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return Response{}, err
	}

	status := StatusAccepted
	if req.Mode == ModeLLM {
		status = StatusQueued
	}
	var mediaIDs []string
	if req.Mode != ModeRecord {
		for _, seg := range req.Segments {
			if media.ValidID(seg.URL) {
				mediaIDs = append(mediaIDs, seg.URL)
			}
		}
	}
	record, err := s.store.ElnisEvents().Create(ctx, storage.CreateElnisEventRequest{
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
	attrs = append(attrs, "event_id", record.ID)
	_ = globalevents.EmitLog(ctx, globalevents.LogRecord{Category: globalevents.LogAudit, Level: slog.LevelInfo, Name: "elnis.accepted", Module: "elnis", Summary: "elnis.accepted", Fields: slog.Group("", attrs...).Value.Group()})
	_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
		Category: globalevents.LogElnis,
		Level:    slog.LevelInfo,
		Name:     "elnis_event_accepted",
		Module:   "elnis",
		Summary:  "elnis event accepted",
		Fields:   slog.Group("", attrs...).Value.Group(),
	})

	switch req.Mode {
	case ModeRecord:
		if err := s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusCompleted, "", ""); err != nil {
			return Response{}, err
		}
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{Category: globalevents.LogAudit, Level: slog.LevelInfo, Name: "elnis.recorded", Module: "elnis", Summary: "elnis.recorded", Fields: slog.Group("", attrs...).Value.Group()})
		return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusCompleted}, nil
	case ModeDirect:
		if err := s.runDirect(ctx, event, record.ID); err != nil {
			_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
				Category: globalevents.LogAudit,
				Level:    slog.LevelWarn,
				Name:     "elnis.direct_failed",
				Module:   "elnis",
				Summary:  "elnis.direct_failed",
				Fields:   slog.Group("", append(attrs, "error", err.Error())...).Value.Group(),
			})
			_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
				Category: globalevents.LogElnis,
				Level:    slog.LevelWarn,
				Name:     "elnis_direct_failed",
				Module:   "elnis",
				Summary:  "elnis direct failed",
				Fields:   slog.Group("", append(attrs, "error", err.Error())...).Value.Group(),
			})
			_ = s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusFailed, "", err.Error())
			return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
		}
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogAudit,
			Level:    slog.LevelInfo,
			Name:     "elnis.direct_completed",
			Module:   "elnis",
			Summary:  "elnis.direct_completed",
			Fields:   slog.Group("", attrs...).Value.Group(),
		})
		return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusCompleted}, nil
	case ModeLLM:
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{Category: globalevents.LogAudit, Level: slog.LevelInfo, Name: "elnis.llm_queued", Module: "elnis", Summary: "elnis.llm_queued", Fields: slog.Group("", attrs...).Value.Group()})
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{Category: globalevents.LogElnis, Level: slog.LevelInfo, Name: "elnis_llm_queued", Module: "elnis", Summary: "elnis llm queued", Fields: slog.Group("", attrs...).Value.Group()})
		if s.enqueueLLM != nil {
			if err := s.enqueueLLM(ctx, QueuedLLMEvent{Event: event, EventID: record.ID}); err != nil {
				_ = s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusFailed, "", err.Error())
				_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
					Category: globalevents.LogElnis,
					Level:    slog.LevelWarn,
					Name:     "elnis_llm_enqueue_failed",
					Module:   "elnis",
					Summary:  "elnis llm enqueue failed",
					Fields:   slog.Group("", append(attrs, "error", err.Error())...).Value.Group(),
				})
				return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
			}
		}
		return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusQueued}, nil
	default:
		return Response{}, fmt.Errorf("unsupported mode %q", req.Mode)
	}
}
