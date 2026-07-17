package elnis

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"elbot/internal/background"
	"elbot/internal/delivery"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

func (s *Service) prepareReport(ctx context.Context, event Event, eventID, resultJSON, sessionID, messageID string, result background.JSONResult) (bool, error) {
	resolved, err := decodeResolvedTargets(event.ResolvedTargets)
	if err != nil {
		return false, err
	}
	outputs, err := background.BuildReportOutputs(result.Report, result.ReportSegments, tool.SandboxContext{
		Dir:            filepath.Join(s.sandboxRoot, filepath.FromSlash(elnisSandboxSubdir(event.Request.Elwisp.Name))),
		Background:     true,
		BackgroundKind: tool.BackgroundKindElnis,
	})
	if err != nil {
		return false, err
	}
	if len(resolved) == 0 || len(outputs) == 0 {
		return false, s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusCompleted, sessionID, resultJSON, "")
	}

	deliveries := make([]storage.CreateElnisReportDeliveryRequest, 0, len(resolved)*len(outputs))
	for _, target := range resolved {
		targetJSON, err := json.Marshal(target)
		if err != nil {
			return false, fmt.Errorf("marshal elnis report target: %w", err)
		}
		for _, output := range outputs {
			outputJSON, err := json.Marshal(output)
			if err != nil {
				return false, fmt.Errorf("marshal elnis report output: %w", err)
			}
			deliveries = append(deliveries, storage.CreateElnisReportDeliveryRequest{
				Target:    string(targetJSON),
				Output:    string(outputJSON),
				MessageID: messageID,
			})
		}
	}
	if err := s.store.ElnisEvents().PrepareReport(ctx, storage.PrepareElnisReportRequest{
		EventID:           eventID,
		ResolvedTargets:   event.ResolvedTargets,
		SessionID:         sessionID,
		Result:            resultJSON,
		Deliveries:        deliveries,
		ResultReadyStatus: StatusResultReady,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) deliverReport(ctx context.Context, eventID string) error {
	if s.send == nil {
		return fmt.Errorf("elnis sender is not configured")
	}
	repo := s.store.ElnisEvents()
	claimed, err := repo.ClaimReport(ctx, eventID, StatusResultReady, StatusDelivering)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	event, err := repo.Get(ctx, eventID)
	if err != nil {
		return s.releaseReport(ctx, eventID, err)
	}
	deliveries, err := repo.ListReportDeliveries(ctx, eventID)
	if err != nil {
		return s.releaseReport(ctx, eventID, err)
	}
	for _, item := range deliveries {
		if item.Status == storage.ElnisReportDeliveryDelivered {
			continue
		}
		var target Target
		if err := json.Unmarshal([]byte(item.Target), &target); err != nil {
			return s.failReportDelivery(ctx, eventID, item.ID, fmt.Errorf("decode elnis report target: %w", err))
		}
		var output delivery.Output
		if err := json.Unmarshal([]byte(item.Output), &output); err != nil {
			return s.failReportDelivery(ctx, eventID, item.ID, fmt.Errorf("decode elnis report output: %w", err))
		}
		if err := repo.StartReportDelivery(ctx, item.ID); err != nil {
			return s.failReportDelivery(ctx, eventID, item.ID, err)
		}
		receipt, err := s.send(ctx, target.ToDeliveryTarget(), output)
		if err != nil {
			return s.failReportDelivery(ctx, eventID, item.ID, err)
		}
		receiptJSON, err := json.Marshal(receipt)
		if err != nil {
			return s.failReportDelivery(ctx, eventID, item.ID, fmt.Errorf("marshal elnis report receipt: %w", err))
		}
		if err := repo.MarkReportDeliveryDelivered(ctx, item.ID, string(receiptJSON)); err != nil {
			return s.releaseReport(ctx, eventID, err)
		}
		s.mapReportReceipt(ctx, event.EventKey, target, event.SessionID, item.MessageID, receipt)
	}
	if err := repo.CompleteReport(ctx, eventID, StatusDelivering, StatusCompleted); err != nil {
		return s.releaseReport(ctx, eventID, err)
	}
	s.auditEvent("elnis.llm_completed", "event_id", eventID, "event_key", event.EventKey, "session_id", event.SessionID)
	s.logInfo("elnis llm completed", "event_id", eventID, "event_key", event.EventKey, "session_id", event.SessionID)
	return nil
}

func (s *Service) failReportDelivery(ctx context.Context, eventID, deliveryID string, deliveryErr error) error {
	if err := s.store.ElnisEvents().MarkReportDeliveryFailed(ctx, eventID, deliveryID, StatusResultReady, deliveryErr.Error()); err != nil {
		return fmt.Errorf("%v; persist delivery failure: %w", deliveryErr, err)
	}
	return deliveryErr
}

func (s *Service) releaseReport(ctx context.Context, eventID string, deliveryErr error) error {
	if err := s.store.ElnisEvents().ReleaseReport(ctx, eventID, StatusDelivering, StatusResultReady, deliveryErr.Error()); err != nil {
		return fmt.Errorf("%v; release report claim: %w", deliveryErr, err)
	}
	return deliveryErr
}

func (s *Service) resetDeliveringReports(ctx context.Context) error {
	return s.store.ElnisEvents().ResetDeliveringReports(ctx, StatusDelivering, StatusResultReady)
}

func (s *Service) recoverReports(ctx context.Context, resetDelivering bool) error {
	repo := s.store.ElnisEvents()
	if resetDelivering {
		if err := s.resetDeliveringReports(ctx); err != nil {
			return err
		}
	}
	ids, err := repo.ListResultReadyReportIDs(ctx, StatusResultReady)
	if err != nil {
		return err
	}
	var firstErr error
	for _, eventID := range ids {
		if err := s.deliverReport(ctx, eventID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			s.logWarn("recover elnis report failed", "event_id", eventID, "error", err.Error())
		}
	}
	return firstErr
}
