// direct.go 管理原生调用、直接投递与事件完成状态。
// 原生调用使用注入的 CallPlatformAPI，显式平台优先，否则使用首个解析目标的平台。
// 调用顺序、失败与取消结果归 Elnis；原生协议转发归 Platform，不在这里恢复能力映射。

package elnis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
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
		platform := strings.TrimSpace(call.Platform)
		if platform == "" && len(targets) > 0 {
			platform = targets[0].Platform
		}
		if _, err := s.callPlatformAPI(ctx, platform, call.API, call.Params); err != nil {
			logResult.ResultStatus = globalevents.ResultFailed
			logResult.Content += fmt.Sprintf("\nplatform=%s api=%s error=%v", platform, call.API, err)
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return logResult, err
		}
		logResult.Content += fmt.Sprintf("\nplatform=%s api=%s completed", platform, call.API)
	}
	return logResult, nil
}

func (s *Service) sendOutputsToTargets(ctx context.Context, targets []Target, outputs []delivery.Output) (globalevents.LogResult, error) {
	ctx = delivery.WithTemporaryConnection(ctx)
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSkipped}
	for _, target := range targets {
		_, result, err := s.send(ctx, toDeliveryTarget(target), outputs)
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
