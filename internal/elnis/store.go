package elnis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"elbot/internal/storage"
)

func (s *Service) handleDuplicate(event Event, existing *storage.ElnisEvent) {
	attrs := s.eventAttrs(event)
	if existing != nil && existing.ContentHash != event.ContentHash {
		s.logWarn("elnis duplicate event hash mismatch", append(attrs, "existing_event_id", existing.ID)...)
	} else {
		s.logInfo("elnis duplicate event", attrs...)
	}
	s.auditEvent("elnis.duplicate", attrs...)
}

func (s *Service) completeEvent(ctx context.Context, id, resolvedTargets, status, result, eventErr string) error {
	return s.completeEventWithSession(ctx, id, resolvedTargets, status, "", result, eventErr)
}

func (s *Service) completeEventWithSession(ctx context.Context, id, resolvedTargets, status, sessionID, result, eventErr string) error {
	return s.store.ElnisEvents().Update(ctx, storage.UpdateElnisEventRequest{ID: id, ResolvedTargets: resolvedTargets, Status: status, SessionID: sessionID, Result: result, Error: eventErr})
}

func (s *Service) eventAttrs(event Event, attrs ...any) []any {
	attrs = append(attrs,
		"origin", event.Origin.Label(),
		"elwisp_name", event.Request.Elwisp.Name,
		"source", event.Request.Source,
		"source_id", event.Request.ID,
		"event_key", event.EventKey,
		"mode", event.Request.Mode,
		"tags", event.TagsJSON,
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

func contentHash(req Request) string {
	data, _ := json.Marshal(req)
	return hashBytes(data)
}

func hashText(value string) string {
	if value == "" {
		return ""
	}
	return hashBytes([]byte(value))
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
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

func setFromStrings(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range trimStrings(values) {
		out[value] = true
	}
	return out
}

func backgroundToolNames(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range trimStrings(values) {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
