package elnis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"elbot/internal/config"
	"elbot/internal/output"
	"elbot/internal/storage"
)

type SenderFunc func(ctx context.Context, target output.Target, out output.Output) error

type AuditFunc func(event string, attrs ...any)

type Options struct {
	Config config.ElnisConfig
	Tokens map[string]string
	Store  storage.Store
	Logger *slog.Logger
	Audit  AuditFunc
	Send   SenderFunc
}

type Service struct {
	cfg    config.ElnisConfig
	tokens map[string]string
	store  storage.Store
	logger *slog.Logger
	audit  AuditFunc
	send   SenderFunc
}

func NewService(opts Options) (*Service, error) {
	if opts.Store == nil || opts.Store.ElnisEvents() == nil {
		return nil, fmt.Errorf("elnis event store is not configured")
	}
	if opts.Config.Enabled && len(opts.Tokens) == 0 {
		return nil, fmt.Errorf("elnis enabled but no tokens are configured")
	}
	return &Service{cfg: opts.Config, tokens: opts.Tokens, store: opts.Store, logger: opts.Logger, audit: opts.Audit, send: opts.Send}, nil
}

func (s *Service) Handle(ctx context.Context, token string, req Request) (Response, error) {
	tokenName, ok := s.authenticate(token)
	if !ok {
		s.auditEvent("elnis.auth_failed")
		return Response{Accepted: false, Status: StatusFailed, Error: "unauthorized"}, fmt.Errorf("unauthorized")
	}
	event, err := s.prepareEvent(tokenName, req)
	if err != nil {
		s.auditEvent("elnis.rejected", "token_name", tokenName, "error", err.Error())
		return Response{Accepted: false, Status: StatusFailed, Error: err.Error()}, err
	}
	attrs := s.eventAttrs(event)
	if err := s.authorizeElwisp(event); err != nil {
		s.auditEvent("elnis.permission_denied", append(attrs, "error", err.Error())...)
		s.logWarn("elnis permission denied", append(attrs, "error", err.Error())...)
		return Response{Accepted: false, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
	}
	if existing, err := s.store.ElnisEvents().GetByKey(ctx, req.Elwisp.Name, req.Source, req.ID); err == nil {
		s.handleDuplicate(event, existing)
		return Response{Accepted: true, Duplicate: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusDuplicate}, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return Response{}, err
	}

	status := StatusAccepted
	result := ""
	eventErr := ""
	if req.Mode == ModeLLM {
		status = StatusUnsupported
		eventErr = "llm mode is not implemented in Elnis phase 1"
	}
	record, err := s.store.ElnisEvents().Create(ctx, storage.CreateElnisEventRequest{
		EventKey:         event.EventKey,
		TokenName:        event.TokenName,
		ElwispName:       req.Elwisp.Name,
		Source:           req.Source,
		SourceID:         req.ID,
		Tags:             event.TagsJSON,
		Mode:             req.Mode,
		ModelSlot:        req.ModelSlot,
		ContentHash:      event.ContentHash,
		RequestedTargets: event.RequestedTargets,
		ResolvedTargets:  event.ResolvedTargets,
		Status:           status,
		Result:           result,
		Error:            eventErr,
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
		s.auditEvent("elnis.llm_unsupported", append(attrs, "error", eventErr)...)
		return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusUnsupported, Error: eventErr}, nil
	default:
		return Response{}, fmt.Errorf("unsupported mode %q", req.Mode)
	}
}

func (s *Service) authenticate(token string) (string, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	for name, value := range s.tokens {
		if value != "" && token == value {
			return name, true
		}
	}
	return "", false
}

func (s *Service) prepareEvent(tokenName string, req Request) (Event, error) {
	req.Version = strings.TrimSpace(req.Version)
	req.Elwisp.Name = strings.TrimSpace(req.Elwisp.Name)
	req.Source = strings.TrimSpace(req.Source)
	req.ID = strings.TrimSpace(req.ID)
	req.Mode = strings.TrimSpace(req.Mode)
	req.Format = strings.TrimSpace(req.Format)
	req.ModelSlot = strings.TrimSpace(req.ModelSlot)
	req.Content = strings.TrimSpace(req.Content)
	if req.Version != "elvena.v1" {
		return Event{}, fmt.Errorf("unsupported ELvena version %q", req.Version)
	}
	if req.Elwisp.Name == "" {
		return Event{}, fmt.Errorf("elwisp.name is required")
	}
	if req.Source == "" {
		return Event{}, fmt.Errorf("source is required")
	}
	if req.ID == "" {
		return Event{}, fmt.Errorf("id is required")
	}
	if req.Content == "" {
		return Event{}, fmt.Errorf("content is required")
	}
	if req.Format == "" {
		req.Format = "text"
	}
	if req.Format != "text" && req.Format != "elyph" {
		return Event{}, fmt.Errorf("unsupported format %q", req.Format)
	}
	if req.Mode != ModeRecord && req.Mode != ModeDirect && req.Mode != ModeLLM {
		return Event{}, fmt.Errorf("unsupported mode %q", req.Mode)
	}
	createdAt := time.Now()
	if strings.TrimSpace(req.CreatedAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(req.CreatedAt))
		if err != nil {
			return Event{}, fmt.Errorf("created_at must be RFC3339: %w", err)
		}
		createdAt = parsed
	}
	tagsJSON, err := json.Marshal(trimStrings(req.Elwisp.Tags))
	if err != nil {
		return Event{}, err
	}
	requestedTargets, err := json.Marshal(normalizeTargets(req.Targets))
	if err != nil {
		return Event{}, err
	}
	resolved, err := s.resolveTargets(req)
	if err != nil {
		return Event{}, err
	}
	resolvedTargets, err := json.Marshal(resolved)
	if err != nil {
		return Event{}, err
	}
	return Event{
		Request:          req,
		TokenName:        tokenName,
		EventKey:         req.Elwisp.Name + "/" + req.Source + "/" + req.ID,
		ContentHash:      contentHash(req),
		TagsJSON:         string(tagsJSON),
		RequestedTargets: string(requestedTargets),
		ResolvedTargets:  string(resolvedTargets),
		CreatedAt:        createdAt,
		ReceivedAt:       time.Now(),
	}, nil
}

func (s *Service) authorizeElwisp(event Event) error {
	policy, ok := s.cfg.Elwisps[event.Request.Elwisp.Name]
	if !ok {
		return nil
	}
	if !policy.Enabled {
		return fmt.Errorf("elwisp %q is disabled", event.Request.Elwisp.Name)
	}
	allowedTokens := trimStrings(policy.AllowedTokens)
	if len(allowedTokens) == 0 {
		return nil
	}
	for _, token := range allowedTokens {
		if token == event.TokenName {
			return nil
		}
	}
	return fmt.Errorf("token %q is not allowed for elwisp %q", event.TokenName, event.Request.Elwisp.Name)
}

func (s *Service) resolveTargets(req Request) (Targets, error) {
	policy := s.cfg.Delivery
	if elwispPolicy, ok := s.cfg.Elwisps[req.Elwisp.Name]; ok {
		if len(elwispPolicy.Delivery.DefaultPlatforms) > 0 {
			policy.DefaultPlatforms = elwispPolicy.Delivery.DefaultPlatforms
		}
		if elwispPolicy.Delivery.AllowSuperadmins {
			policy.AllowSuperadmins = true
		}
	}
	allowed := setFromStrings(policy.DefaultPlatforms)
	requested := trimStrings(req.Targets.Platforms)
	if len(requested) == 0 {
		requested = trimStrings(policy.DefaultPlatforms)
	}
	platforms := []string{}
	for _, platform := range requested {
		if allowed[platform] {
			platforms = append(platforms, platform)
		}
	}
	platforms = uniqueSorted(platforms)
	if len(platforms) == 0 && req.Mode == ModeDirect {
		return Targets{}, fmt.Errorf("no allowed delivery platforms")
	}
	return Targets{Platforms: platforms, Superadmins: policy.AllowSuperadmins && (req.Targets.Superadmins || req.Mode == ModeDirect)}, nil
}

func (s *Service) handleDuplicate(event Event, existing *storage.ElnisEvent) {
	attrs := s.eventAttrs(event)
	if existing != nil && existing.ContentHash != event.ContentHash {
		s.logWarn("elnis duplicate event hash mismatch", append(attrs, "existing_event_id", existing.ID)...)
	} else {
		s.logInfo("elnis duplicate event", attrs...)
	}
	s.auditEvent("elnis.duplicate", attrs...)
}

func (s *Service) runDirect(ctx context.Context, event Event, eventID string) error {
	if s.send == nil {
		return fmt.Errorf("elnis sender is not configured")
	}
	text := directText(event.Request)
	resolved := Targets{}
	if err := json.Unmarshal([]byte(event.ResolvedTargets), &resolved); err != nil {
		return err
	}
	if !resolved.Superadmins {
		return fmt.Errorf("direct delivery only supports superadmins in phase 1")
	}
	for _, platformName := range resolved.Platforms {
		if err := s.send(ctx, output.Target{Platform: platformName, Superadmins: true}, output.Text(text)); err != nil {
			return err
		}
	}
	return s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusCompleted, "", "")
}

func (s *Service) completeEvent(ctx context.Context, id, resolvedTargets, status, result, eventErr string) error {
	return s.store.ElnisEvents().Update(ctx, storage.UpdateElnisEventRequest{ID: id, ResolvedTargets: resolvedTargets, Status: status, Result: result, Error: eventErr})
}

func directText(req Request) string {
	parts := []string{}
	if title := strings.TrimSpace(req.Title); title != "" {
		parts = append(parts, title)
	}
	parts = append(parts, strings.TrimSpace(req.Content))
	return strings.Join(parts, "\n")
}

func contentHash(req Request) string {
	data, _ := json.Marshal(req)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func normalizeTargets(targets Targets) Targets {
	return Targets{Platforms: uniqueSorted(targets.Platforms), Superadmins: targets.Superadmins}
}

func trimStrings(values []string) []string {
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range trimStrings(values) {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func setFromStrings(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range trimStrings(values) {
		out[value] = true
	}
	return out
}

func (s *Service) eventAttrs(event Event, attrs ...any) []any {
	attrs = append(attrs,
		"token_name", event.TokenName,
		"elwisp_name", event.Request.Elwisp.Name,
		"source", event.Request.Source,
		"source_id", event.Request.ID,
		"event_key", event.EventKey,
		"mode", event.Request.Mode,
	)
	return attrs
}

func (s *Service) auditEvent(event string, attrs ...any) {
	if s.audit != nil {
		s.audit(event, attrs...)
	}
}

func (s *Service) logInfo(msg string, attrs ...any) {
	if s.logger != nil {
		s.logger.Info(msg, attrs...)
	}
}

func (s *Service) logWarn(msg string, attrs ...any) {
	if s.logger != nil {
		s.logger.Warn(msg, attrs...)
	}
}
