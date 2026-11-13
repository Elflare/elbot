// direct.go 管理原生调用、直接投递与事件完成状态。
// 原生调用使用注入的 CallPlatformAPI，显式平台优先，否则使用首个解析目标的平台。
// 调用顺序、失败与取消结果归 Elnis；原生协议转发归 Platform，不在这里恢复能力映射。

package elnis

import (
	"context"
	"elbot/internal/operationresult"
	"strings"

	"elbot/internal/delivery"
)

// runDirect coordinates direct execution; each operation owns its diagnostics.
func (s *Service) runDirect(ctx context.Context, event Event, eventID string) (operationresult.Result[struct{}], error) {
	req := event.Request
	resolved, err := decodeResolvedTargets(event.ResolvedTargets)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	err = s.executeCalls(ctx, resolved, req.Calls)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if len(resolved) > 0 && (strings.TrimSpace(req.Content) != "" || len(req.Segments) > 0) {
		outputs, err := s.directMediaOutputs(ctx, event, eventID)
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		if outputs.Status != operationresult.Success {
			return operationresult.Result[struct{}]{Status: outputs.Status, Message: outputs.Message}, nil
		}
		sent, err := s.sendOutputsToTargets(ctx, resolved, outputs.Data)
		if err != nil || sent.Status != operationresult.Success {
			return sent, err
		}
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusCompleted, "", "")
}

func (s *Service) executeCalls(ctx context.Context, targets []Target, calls []Call) error {
	for _, call := range calls {
		platform := strings.TrimSpace(call.Platform)
		if platform == "" && len(targets) > 0 {
			platform = targets[0].Platform
		}
		if _, err := s.callPlatformAPI(ctx, platform, call.API, call.Params); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) sendOutputsToTargets(ctx context.Context, targets []Target, outputs []delivery.Output) (operationresult.Result[struct{}], error) {
	ctx = delivery.WithTemporaryConnection(ctx)
	for _, target := range targets {
		sent, err := s.send(ctx, toDeliveryTarget(target), outputs)
		if err != nil {
			return operationresult.Result[struct{}]{}, err
		}
		if sent.Status != operationresult.Success {
			return operationresult.Result[struct{}]{Status: sent.Status, Message: sent.Message}, nil
		}
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}
