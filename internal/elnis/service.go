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
	"elbot/internal/storage"
)

type SenderFunc func(ctx context.Context, target delivery.Target, out delivery.Output) (delivery.Receipt, error)

type AuditFunc func(event string, attrs ...any)

type QueuedLLMEvent struct {
	Event   Event
	EventID string
}

type EnqueueLLMFunc func(ctx context.Context, event QueuedLLMEvent) error

type ModelResolverFunc func(slot string) config.ModelSelection

type Options struct {
	Config           config.ElnisConfig
	SandboxRoot      string
	Tokens           map[string]string
	Store            storage.Store
	Logger           *slog.Logger
	Audit            AuditFunc
	Send             SenderFunc
	Runner           background.Runner
	ResolveModel     ModelResolverFunc
	EnabledPlatforms []string
	PlatformCallers  elvena.PlatformCallerResolver
}

type Service struct {
	cfg              config.ElnisConfig
	sandboxRoot      string
	tokens           map[string]string
	store            storage.Store
	logger           *slog.Logger
	audit            AuditFunc
	send             SenderFunc
	runner           background.Runner
	resolveModel     ModelResolverFunc
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
		cfg:              opts.Config,
		sandboxRoot:      opts.SandboxRoot,
		tokens:           opts.Tokens,
		store:            opts.Store,
		logger:           opts.Logger,
		audit:            opts.Audit,
		send:             opts.Send,
		runner:           opts.Runner,
		resolveModel:     opts.ResolveModel,
		enabledPlatforms: uniqueSorted(opts.EnabledPlatforms),
		platformCallers:  opts.PlatformCallers,
	}, nil
}

func (s *Service) SetLLMEnqueuer(enqueue EnqueueLLMFunc) {
	s.enqueueLLM = enqueue
}

func (s *Service) Handle(ctx context.Context, token string, req Request) (Response, error) {
	tokenName, ok := s.authenticate(token)
	if !ok {
		s.auditEvent("elnis.auth_failed")
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
		s.auditEvent("elnis.rejected", "origin", origin.Label(), "error", err.Error())
		return Response{Accepted: false, Status: StatusFailed, Error: err.Error()}, err
	}
	return s.handlePreparedEvent(ctx, event)
}

func (s *Service) handlePreparedEvent(ctx context.Context, event Event) (Response, error) {
	req := event.Request
	attrs := s.eventAttrs(event)
	if err := s.authorizeElwisp(event); err != nil {
		s.auditEvent("elnis.permission_denied", append(attrs, "error", err.Error())...)
		s.logWarn("elnis permission denied", append(attrs, "error", err.Error())...)
		return Response{Accepted: false, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
	}
	if err := s.authorizeInternalTools(event); err != nil {
		s.auditEvent("elnis.tool_denied", append(attrs, "error", err.Error())...)
		s.logWarn("elnis internal tool denied", append(attrs, "error", err.Error())...)
		return Response{Accepted: false, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
	}
	if err := s.authorizeExternalTools(event); err != nil {
		s.auditEvent("elnis.external_tool_denied", append(attrs, "error", err.Error())...)
		s.logWarn("elnis external tool denied", append(attrs, "error", err.Error())...)
		return Response{Accepted: false, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
	}
	if existing, err := s.store.ElnisEvents().GetByKey(ctx, req.Elwisp.Name, req.Source, req.ID); err == nil {
		s.handleDuplicate(event, existing)
		return Response{Accepted: true, Duplicate: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusDuplicate}, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return Response{}, err
	}

	status := StatusAccepted
	if req.Mode == ModeLLM {
		status = StatusQueued
	}
	record, err := s.store.ElnisEvents().Create(ctx, storage.CreateElnisEventRequest{
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
	s.auditEvent("elnis.accepted", attrs...)
	s.logInfo("elnis event accepted", attrs...)

	switch req.Mode {
	case ModeRecord:
		if err := s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusCompleted, "", ""); err != nil {
			return Response{}, err
		}
		s.auditEvent("elnis.recorded", attrs...)
		return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusCompleted}, nil
	case ModeDirect:
		if err := s.runDirect(ctx, event, record.ID); err != nil {
			s.auditEvent("elnis.direct_failed", append(attrs, "error", err.Error())...)
			s.logWarn("elnis direct failed", append(attrs, "error", err.Error())...)
			_ = s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusFailed, "", err.Error())
			return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
		}
		s.auditEvent("elnis.direct_completed", attrs...)
		return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusCompleted}, nil
	case ModeLLM:
		s.auditEvent("elnis.llm_queued", attrs...)
		s.logInfo("elnis llm queued", attrs...)
		if s.enqueueLLM != nil {
			if err := s.enqueueLLM(ctx, QueuedLLMEvent{Event: event, EventID: record.ID}); err != nil {
				_ = s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusFailed, "", err.Error())
				s.logWarn("elnis llm enqueue failed", append(attrs, "error", err.Error())...)
				return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
			}
		}
		return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusQueued}, nil
	default:
		return Response{}, fmt.Errorf("unsupported mode %q", req.Mode)
	}
}
