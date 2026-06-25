package elnis

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"elbot/internal/background"
	"elbot/internal/delivery"
	"elbot/internal/elvena"
	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

func (s *Service) runDirect(ctx context.Context, event Event, eventID string) error {
	if s.send == nil {
		return fmt.Errorf("elnis sender is not configured")
	}
	req := event.Request
	paths, err := s.downloadSegments(ctx, req.Elwisp.Name, req.ID, req.Segments)
	if err != nil {
		return fmt.Errorf("download segments: %w", err)
	}
	resolved, err := decodeResolvedTargets(event.ResolvedTargets)
	if err != nil {
		return err
	}
	if err := s.executeCalls(ctx, resolved, req.Calls); err != nil {
		return err
	}
	if strings.TrimSpace(req.Content) != "" || len(req.Segments) > 0 {
		outputs := elvena.BuildDirectOutputs(req, paths)
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

func (s *Service) sendReport(ctx context.Context, event Event, report string, reportSegments []llm.MessageSegment, sessionID, messageID string) error {
	if s.send == nil {
		return fmt.Errorf("elnis sender is not configured")
	}
	resolved, err := decodeResolvedTargets(event.ResolvedTargets)
	if err != nil {
		return err
	}
	if len(resolved) == 0 {
		return nil
	}
	outputs, err := background.BuildReportOutputs(report, reportSegments, tool.SandboxContext{Dir: filepath.Join(s.sandboxRoot, filepath.FromSlash(elnisSandboxSubdir(event.Request.Elwisp.Name))), Background: true, BackgroundKind: tool.BackgroundKindElnis})
	if err != nil {
		return err
	}
	return s.sendOutputsToTargetsMapped(ctx, event.EventKey, resolved, outputs, sessionID, messageID)
}

func (s *Service) sendOutputsToTargets(ctx context.Context, targets []Target, outputs []delivery.Output) error {
	return s.sendOutputsToTargetsMapped(ctx, "", targets, outputs, "", "")
}

func (s *Service) sendOutputsToTargetsMapped(ctx context.Context, eventKey string, targets []Target, outputs []delivery.Output, sessionID, messageID string) error {
	for _, target := range targets {
		deliveryTarget := target.ToDeliveryTarget()
		for _, out := range outputs {
			receipt, err := s.send(ctx, deliveryTarget, out)
			if err != nil {
				return err
			}
			s.mapReportReceipt(ctx, eventKey, target, sessionID, messageID, receipt)
		}
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
