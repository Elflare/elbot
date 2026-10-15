package elnis

import (
	"context"
	"crypto/sha256"
	"elbot/internal/background"
	"elbot/internal/contextinfo"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	globalevents "elbot/internal/events"
	"elbot/internal/storage"
)

func (s *Service) handleDuplicate(ctx context.Context, event Event, existing *storage.ElnisEvent) {
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

	if existing.ContentHash != event.ContentHash {
		{
			content, _ := json.Marshal(map[string]any{"origin": event.Origin.Label(), "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "existing_event_id": existing.ID})
			record := globalevents.LogRecord{
				Category:     globalevents.LogElnis,
				Level:        slog.LevelWarn,
				EventName:    "elnis_duplicate_event_hash_mismatch",
				Module:       "elnis",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ResultStatus: globalevents.ResultSkipped,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
	} else {
		{
			content, _ := json.Marshal(map[string]any{"origin": event.Origin.Label(), "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags})
			record := globalevents.LogRecord{
				Category:     globalevents.LogElnis,
				Level:        slog.LevelInfo,
				EventName:    "elnis_duplicate_event",
				Module:       "elnis",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ResultStatus: globalevents.ResultSkipped,
			}
			record.Identity = identity
			background.LogBackground(ctx, record)
		}
	}
	{
		content, _ := json.Marshal(map[string]any{"origin": event.Origin.Label(), "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags})
		record := globalevents.LogRecord{
			Category:     globalevents.LogAudit,
			Level:        slog.LevelInfo,
			EventName:    "elnis.duplicate",
			Module:       "elnis",
			Type:         globalevents.ContentJSON,
			Content:      string(content),
			ResultStatus: globalevents.ResultSkipped,
		}
		record.Identity = identity
		background.LogBackground(ctx, record)
	}
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
