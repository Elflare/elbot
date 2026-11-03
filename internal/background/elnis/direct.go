// direct.go 管理原生调用、直接投递与事件完成状态。
// 原生调用使用注入的 CallPlatformAPI，显式平台优先，否则使用首个解析目标的平台。
// 调用顺序、失败与取消结果归 Elnis；原生协议转发归 Platform，不在这里恢复能力映射。

package elnis

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"fmt"
	"log/slog"
	"strings"

	"elbot/internal/delivery"
)

// runDirect coordinates direct execution; each operation owns its diagnostics.
func (s *Service) runDirect(ctx context.Context, event Event, eventID string) error {
	req := event.Request
	resolved, err := decodeResolvedTargets(event.ResolvedTargets)
	if err != nil {
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		_ = events.EmitLog(ctx, events.LogRecord{Category: events.LogElnis, Level: slog.LevelError, Module: "elnis", EventName: "elnis_direct_failed", ResultStatus: events.ResultFailed, Identity: events.LogIdentityFromExecution(facts), Content: fmt.Sprintf("event_id=%s error=%v", eventID, err)})
		return err
	}
	err = s.executeCalls(ctx, resolved, req.Calls)
	if err != nil {
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
	for _, call := range calls {
		platform := strings.TrimSpace(call.Platform)
		if platform == "" && len(targets) > 0 {
			platform = targets[0].Platform
		}
		if _, err := s.callPlatformAPI(ctx, platform, call.API, call.Params); err != nil {
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			record := events.LogRecord{Category: events.LogElnis, Level: slog.LevelWarn, Module: "elnis", EventName: "elnis_platform_call_failed", ResultStatus: events.LogStatus(err), Identity: events.LogIdentityFromExecution(facts), Content: fmt.Sprintf("platform=%s api=%s error=%v", platform, call.API, err)}
			if record.ResultStatus == events.ResultCanceled {
				record.Level = slog.LevelInfo
			}
			_ = events.EmitLog(ctx, record)
			return err
		}
	}
	return nil
}

func (s *Service) sendOutputsToTargets(ctx context.Context, targets []Target, outputs []delivery.Output) error {
	ctx = delivery.WithTemporaryConnection(ctx)
	for _, target := range targets {
		_, err := s.send(ctx, toDeliveryTarget(target), outputs)
		if err != nil {
			return err
		}
	}
	return nil
}
