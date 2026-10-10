package elnis

import (
	"context"
	"elbot/internal/background"
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

// runDirect declares this event's result before its caller saves failure state.
// Sending and event completion retain their business errors independently.
func (s *Service) runDirect(ctx context.Context, event Event, eventID string) (globalevents.LogResult, error) {
	if s.send == nil {
		err := fmt.Errorf("elnis sender is not configured")
		return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	req := event.Request
	resolved, err := decodeResolvedTargets(event.ResolvedTargets)
	if err != nil {
		return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	logResult, err := s.executeCalls(ctx, resolved, req.Calls)
	if err != nil {
		return logResult, err
	}
	if len(resolved) > 0 && (strings.TrimSpace(req.Content) != "" || len(req.Segments) > 0) {
		outputs, err := s.directMediaOutputs(ctx, event, eventID)
		if err != nil {
			logResult.ResultStatus = globalevents.ResultFailed
			logResult.Content += fmt.Sprintf("\nmedia_error=%v", err)
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return logResult, err
		}
		result, err := s.sendOutputsToTargets(ctx, resolved, outputs)
		logResult.Content += "\nsend:\n" + result.Content
		logResult.ResultStatus = result.ResultStatus
		if err != nil {
			return logResult, err
		}
	}
	if err := s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusCompleted, "", ""); err != nil {
		logResult.Content += fmt.Sprintf("\npersistence_error=%v", err)
		if logResult.ResultStatus != globalevents.ResultFailed {
			logResult.ResultStatus = globalevents.ResultFailed
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
		}
		return logResult, err
	}
	if logResult.ResultStatus == globalevents.ResultSkipped {
		logResult.ResultStatus = globalevents.ResultSucceeded
	}
	logResult.Content += "\nevent state completed"
	return logResult, nil
}

func (s *Service) executeCalls(ctx context.Context, targets []Target, calls []Call) (globalevents.LogResult, error) {
	if len(calls) == 0 {
		return globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "no platform API calls"}, nil
	}
	if s.platformCallers == nil {
		err := fmt.Errorf("platform api callers are not configured")
		return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}
	for _, call := range calls {
		rawCall, err := elvena.ResolveRawCall(call)
		if err != nil {
			logResult.ResultStatus = globalevents.ResultFailed
			logResult.Content += "\n" + err.Error()
			return logResult, err
		}
		platform := strings.TrimSpace(rawCall.Platform)
		if platform == "" && len(targets) > 0 {
			platform = targets[0].Platform
		}
		caller, ok := s.platformCallers.PlatformCaller(platform)
		if !ok || caller == nil {
			err := fmt.Errorf("platform %q does not support api calls", platform)
			logResult.ResultStatus = globalevents.ResultFailed
			logResult.Content += "\n" + err.Error()
			return logResult, err
		}
		if _, err := caller.CallPlatformAPI(ctx, rawCall.API, rawCall.Params); err != nil {
			logResult.ResultStatus = globalevents.ResultFailed
			logResult.Content += fmt.Sprintf("\nplatform=%s api=%s error=%v", platform, rawCall.API, err)
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return logResult, err
		}
		logResult.Content += fmt.Sprintf("\nplatform=%s api=%s completed", platform, rawCall.API)
	}
	return logResult, nil
}

func (s *Service) sendOutputsToTargets(ctx context.Context, targets []Target, outputs []delivery.Output) (globalevents.LogResult, error) {
	return s.sendOutputsToTargetsMapped(ctx, "", targets, outputs, "", "")
}

func (s *Service) sendOutputsToTargetsMapped(ctx context.Context, eventKey string, targets []Target, outputs []delivery.Output, sessionID, messageID string) (globalevents.LogResult, error) {
	ctx = delivery.WithTemporaryConnection(ctx)
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSkipped}
	for _, target := range targets {
		receipt, result, err := s.send(ctx, target.ToDeliveryTarget(), outputs)
		s.mapReportReceipt(ctx, eventKey, sessionID, messageID, receipt)
		logResult.Content += fmt.Sprintf("\ntarget=%v\n%s", target, result.Content)
		if logResult.ResultStatus == globalevents.ResultSkipped || result.ResultStatus == globalevents.ResultFailed ||
			(result.ResultStatus == globalevents.ResultCanceled && logResult.ResultStatus != globalevents.ResultFailed) {
			logResult.ResultStatus = result.ResultStatus
		}
		if err != nil {
			return logResult, err
		}
	}
	return logResult, nil
}

func (s *Service) mapReportReceipt(ctx context.Context, eventKey, sessionID, messageID string, receipt delivery.Receipt) {
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
				record.Identity = identity
				record.Identity.SessionID = &sessionID
				background.LogBackground(ctx, record)
				record.Category, record.EventName = globalevents.LogElnis, "map_elnis_report_message_failed"
				background.LogBackground(ctx, record)
			}
		}
	}
}
