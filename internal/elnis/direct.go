package elnis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	"elbot/internal/elvena"
	globalevents "elbot/internal/events"
	"elbot/internal/storage"
)

// runDirect declares this event's result before its caller saves failure state.
// Sending and event completion retain their business errors independently.
func (s *Service) runDirect(ctx context.Context, event Event, eventID string) (globalevents.LogResult, error) {
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
	ctx = delivery.WithTemporaryConnection(ctx)
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSkipped}
	for _, target := range targets {
		_, result, err := s.send(ctx, target.ToDeliveryTarget(), outputs)
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

func (s *Service) mapReportReceipt(sessionID, messageID string, receipt delivery.Receipt) []storage.PlatformMessageMap {
	if sessionID == "" || messageID == "" {
		return nil
	}
	mappings := make([]storage.PlatformMessageMap, 0, len(receipt.SentMessages))
	for _, sent := range receipt.SentMessages {
		mappings = append(mappings, storage.PlatformMessageMap{Platform: sent.Platform, PlatformScopeID: sent.ScopeID, PlatformMessageID: sent.PlatformMessageID, SessionID: sessionID, MessageID: messageID})
	}
	return mappings
}
