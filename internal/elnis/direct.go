package elnis

import (
	"context"
	"elbot/internal/contextinfo"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/elvena"
	globalevents "elbot/internal/events"
	"elbot/internal/storage"
)

func (s *Service) runDirect(ctx context.Context, event Event, eventID string) error {
	if s.send == nil {
		return fmt.Errorf("elnis sender is not configured")
	}
	req := event.Request
	resolved, err := decodeResolvedTargets(event.ResolvedTargets)
	if err != nil {
		return err
	}
	if err := s.executeCalls(ctx, resolved, req.Calls); err != nil {
		return err
	}
	if len(resolved) > 0 && (strings.TrimSpace(req.Content) != "" || len(req.Segments) > 0) {
		outputs, err := s.directMediaOutputs(ctx, event, eventID)
		if err != nil {
			return err
		}
		if err := s.sendOutputsToTargets(ctx, resolved, outputs); err != nil {
			return err
		}
	}
	return s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusCompleted, "", "")
}

func (s *Service) executeCalls(ctx context.Context, targets []Target, calls []Call) error {
	if len(calls) == 0 {
		return nil
	}
	if s.platformCallers == nil {
		return fmt.Errorf("platform api callers are not configured")
	}
	for _, call := range calls {
		rawCall, err := elvena.ResolveRawCall(call)
		if err != nil {
			return err
		}
		platform := strings.TrimSpace(rawCall.Platform)
		if platform == "" && len(targets) > 0 {
			platform = targets[0].Platform
		}
		caller, ok := s.platformCallers.PlatformCaller(platform)
		if !ok || caller == nil {
			return fmt.Errorf("platform %q does not support api calls", platform)
		}
		if _, err := caller.CallPlatformAPI(ctx, rawCall.API, rawCall.Params); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) sendOutputsToTargets(ctx context.Context, targets []Target, outputs []delivery.Output) error {
	return s.sendOutputsToTargetsMapped(ctx, "", targets, outputs, "", "")
}

func (s *Service) sendOutputsToTargetsMapped(ctx context.Context, eventKey string, targets []Target, outputs []delivery.Output, sessionID, messageID string) error {
	ctx = delivery.WithTemporaryConnection(ctx)
	for _, target := range targets {
		receipt, err := s.send(ctx, target.ToDeliveryTarget(), outputs)
		s.mapReportReceipt(ctx, eventKey, sessionID, messageID, receipt)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) mapReportReceipt(ctx context.Context, eventKey, sessionID, messageID string, receipt delivery.Receipt) {
	if sessionID == "" || messageID == "" || s.store == nil || s.store.Messages() == nil {
		return
	}
	for _, sent := range receipt.SentMessages {
		platformName, scopeID, platformMessageID := strings.TrimSpace(sent.Platform), strings.TrimSpace(sent.ScopeID), strings.TrimSpace(sent.PlatformMessageID)
		if platformName == "" || scopeID == "" || platformMessageID == "" {
			continue
		}
		mapping := storage.PlatformMessageMap{Platform: platformName, PlatformScopeID: scopeID, PlatformMessageID: platformMessageID, SessionID: sessionID, MessageID: messageID}
		if err := s.store.Messages().MapPlatformMessage(ctx, mapping); err != nil {
			{
				content, _ := json.Marshal(map[string]any{"event_key": eventKey, "platform": platformName, "scope_id": scopeID, "platform_message_id": platformMessageID, "message_id": messageID, "error": fmt.Sprintf("%.2048s", err.Error())})
				record := globalevents.LogRecord{
					Category:     globalevents.LogAudit,
					Level:        slog.LevelError,
					EventName:    "elnis.report_map_failed",
					Module:       "elnis",
					Type:         globalevents.ContentJSON,
					Content:      string(content),
					ResultStatus: globalevents.ResultFailed,
				}
				if errors.Is(err, context.Canceled) {
					record.ResultStatus = globalevents.ResultCanceled
					record.Level = slog.LevelInfo
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
				record.Identity.SessionID = &sessionID
				_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
			}
			{
				content, _ := json.Marshal(map[string]any{"event_key": eventKey, "platform": platformName, "scope_id": scopeID, "platform_message_id": platformMessageID, "message_id": messageID, "error": fmt.Sprintf("%.2048s", err.Error())})
				record := globalevents.LogRecord{
					Category:     globalevents.LogElnis,
					Level:        slog.LevelError,
					EventName:    "map_elnis_report_message_failed",
					Module:       "elnis",
					Type:         globalevents.ContentJSON,
					Content:      string(content),
					ResultStatus: globalevents.ResultFailed,
				}
				if errors.Is(err, context.Canceled) {
					record.ResultStatus = globalevents.ResultCanceled
					record.Level = slog.LevelInfo
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
				record.Identity.SessionID = &sessionID
				_ = globalevents.EmitLog(context.WithoutCancel(ctx), record)
			}
		}
	}
}
