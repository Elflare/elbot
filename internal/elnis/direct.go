package elnis

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/elvena"
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
		if err != nil {
			return err
		}
		if err := s.cacheMediaReceipt(ctx, target, outputs, receipt); err != nil {
			return err
		}
		s.mapReportReceipt(ctx, eventKey, target, sessionID, messageID, receipt)
	}
	return nil
}

func (s *Service) mapReportReceipt(ctx context.Context, eventKey string, target Target, sessionID, messageID string, receipt delivery.Receipt) {
	if sessionID == "" || messageID == "" || s.store == nil || s.store.Messages() == nil {
		return
	}
	scopeID := elvena.TargetScopeID(target)
	if scopeID == "" {
		return
	}
	for _, platformMessageID := range receipt.PlatformMessageIDs {
		platformMessageID = strings.TrimSpace(platformMessageID)
		if platformMessageID == "" {
			continue
		}
		mapping := storage.PlatformMessageMap{Platform: target.Platform, PlatformScopeID: scopeID, PlatformMessageID: platformMessageID, SessionID: sessionID, MessageID: messageID}
		if err := s.store.Messages().MapPlatformMessage(ctx, mapping); err != nil {
			s.auditEvent("elnis.report_map_failed", "event_key", eventKey, "platform", target.Platform, "scope_id", scopeID, "platform_message_id", platformMessageID, "session_id", sessionID, "message_id", messageID, "error", err.Error())
			s.logWarn("map elnis report message failed", "event_key", eventKey, "platform", target.Platform, "scope_id", scopeID, "platform_message_id", platformMessageID, "session_id", sessionID, "message_id", messageID, "error", err.Error())
		}
	}
}
