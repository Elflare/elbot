package elnis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	globalevents "elbot/internal/events"
	"elbot/internal/storage"
)

func (s *Service) handleDuplicate(ctx context.Context, event Event, existing *storage.ElnisEvent) {
	attrs := s.eventAttrs(event)
	if existing != nil && existing.ContentHash != event.ContentHash {
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogElnis,
			Level:    slog.LevelWarn,
			Name:     "elnis_duplicate_event_hash_mismatch",
			Module:   "elnis",
			Summary:  "elnis duplicate event hash mismatch",
			Fields:   slog.Group("", append(attrs, "existing_event_id", existing.ID)...).Value.Group(),
		})
	} else {
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogElnis,
			Level:    slog.LevelInfo,
			Name:     "elnis_duplicate_event",
			Module:   "elnis",
			Summary:  "elnis duplicate event",
			Fields:   slog.Group("", attrs...).Value.Group(),
		})
	}
	_ = globalevents.EmitLog(ctx, globalevents.LogRecord{Category: globalevents.LogAudit, Level: slog.LevelInfo, Name: "elnis.duplicate", Module: "elnis", Summary: "elnis.duplicate", Fields: slog.Group("", attrs...).Value.Group()})
}

func (s *Service) completeEvent(ctx context.Context, id, resolvedTargets, status, result, eventErr string) error {
	return s.completeEventWithSession(ctx, id, resolvedTargets, status, "", result, eventErr)
}

func (s *Service) completeEventWithSession(ctx context.Context, id, resolvedTargets, status, sessionID, result, eventErr string) error {
	if status == StatusFailed || status == StatusCompleted || status == StatusTakenOver {
		// Persist terminal state and release event references even after cancellation.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		ctx = cleanupCtx
	}
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
