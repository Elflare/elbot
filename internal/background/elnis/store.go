package elnis

import (
	"context"
	"crypto/sha256"
	background "elbot/internal/background/share"
	"elbot/internal/contextinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	globalevents "elbot/internal/events"
)

func (s *Service) handleDuplicate(ctx context.Context, event Event, existing *EventRecord) {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	identity := globalevents.LogIdentityFromExecution(facts)

	if existing.ContentHash != event.ContentHash {
		content, _ := json.Marshal(map[string]any{"origin": event.Origin, "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags, "existing_event_id": existing.ID})
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
	} else {
		content, _ := json.Marshal(map[string]any{"origin": event.Origin, "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags})
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
	{
		content, _ := json.Marshal(map[string]any{"origin": event.Origin, "elwisp_name": event.Request.Elwisp.Name, "source": event.Request.Source, "source_id": event.Request.ID, "event_key": event.EventKey, "mode": event.Request.Mode, "tags": event.Request.Elwisp.Tags})
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

func (s *Service) completeEventWithSession(ctx context.Context, id, resolvedTargets, status, sessionID, result, eventErr string) (err error) {
	defer func() {
		if err == nil {
			return
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		record := globalevents.LogRecord{Category: globalevents.LogElnis, Level: slog.LevelError, Module: "elnis", EventName: "elnis_event_save_failed", ResultStatus: globalevents.ResultFailed, Identity: globalevents.LogIdentityFromExecution(facts), Content: fmt.Sprintf("event_id=%s status=%s error=%v", id, status, err)}
		record.Identity.SessionID = sessionID
		if globalevents.LogStatus(err) == globalevents.ResultCanceled {
			record.Level, record.ResultStatus = slog.LevelInfo, globalevents.ResultCanceled
		}
		background.LogBackground(ctx, record)
	}()
	if status == StatusFailed || status == StatusCompleted || status == StatusTakenOver {
		// Persist terminal state and release event references even after cancellation.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		ctx = cleanupCtx
	}
	return s.store.Update(ctx, UpdateEventRequest{ID: id, ResolvedTargets: resolvedTargets, Status: status, SessionID: sessionID, Result: result, Error: eventErr})
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
