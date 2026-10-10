package elnis

import (
	"context"
	"elbot/internal/contextinfo"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"elbot/internal/background"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/storage"
)

func (s *Service) prepareReport(ctx context.Context, event Event, eventID, resultJSON, sessionID, messageID string, result background.JSONResult) (bool, error) {
	if taken, err := background.SessionTakenOver(ctx, s.store, sessionID); err != nil {
		return false, err
	} else if taken {
		return false, s.completeTakeover(ctx, eventID, event.ResolvedTargets, background.RunResult{SessionID: sessionID, MessageID: messageID, TakenOver: true, Outcome: "taken_over", Text: resultJSON}, nil)
	}
	resolved, err := decodeResolvedTargets(event.ResolvedTargets)
	if err != nil {
		return false, err
	}
	if len(resolved) == 0 {
		return false, s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusCompleted, sessionID, resultJSON, "")
	}
	if s.media != nil {
		result.ReportSegments, err = s.importReportSegments(ctx, result.ReportSegments, eventID, filepath.Join(s.sandboxRoot, filepath.FromSlash(elnisSandboxSubdir(event.Request.Elwisp.Name))))
		if err != nil {
			return false, err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return false, err
		}
		resultJSON = string(encoded)
	}
	outputs, err := background.BuildReportOutputs(result.Report, result.ReportSegments, sandboxctx.SandboxContext{
		Dir:            filepath.Join(s.sandboxRoot, filepath.FromSlash(elnisSandboxSubdir(event.Request.Elwisp.Name))),
		Background:     true,
		BackgroundKind: sandboxctx.BackgroundKindElnis,
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

// deliverReport owns this report's send/save outcome, independent of retry state.
func (s *Service) deliverReport(ctx context.Context, eventID string) (logResult globalevents.LogResult, err error) {
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

	logResult = globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "event_id=" + eventID}
	defer func() {
		if err != nil {
			logResult.Content += fmt.Sprintf("\nreport_error=%v", err)
		}
	}()
	if s.send == nil {
		return logResult, fmt.Errorf("elnis sender is not configured")
	}
	repo := s.store.ElnisEvents()
	claimed, err := repo.ClaimReport(ctx, eventID, StatusResultReady, StatusDelivering)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return logResult, err
	}
	if !claimed {
		logResult.ResultStatus = globalevents.ResultSkipped
		logResult.Content += "\nreport is not claimable"
		return logResult, nil
	}
	event, err := repo.Get(ctx, eventID)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return s.releaseReport(ctx, eventID, logResult, err)
	}
	deliveries, err := repo.ListReportDeliveries(ctx, eventID)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return s.releaseReport(ctx, eventID, logResult, err)
	}
	logResult.ResultStatus = globalevents.ResultSucceeded
	for index := 0; index < len(deliveries); {
		if taken, err := background.SessionTakenOver(ctx, s.store, event.SessionID); err != nil {
			if logResult.ResultStatus != globalevents.ResultFailed {
				logResult.ResultStatus = globalevents.ResultFailed
				if errors.Is(err, context.Canceled) {
					logResult.ResultStatus = globalevents.ResultCanceled
				}
			}
			return s.releaseReport(ctx, eventID, logResult, err)
		} else if taken {
			err := s.completeTakeover(ctx, eventID, event.ResolvedTargets, background.RunResult{SessionID: event.SessionID, TakenOver: true, Outcome: "taken_over", Text: event.Result}, nil)
			if err != nil {
				logResult.ResultStatus = globalevents.ResultFailed
			} else if logResult.ResultStatus == globalevents.ResultSucceeded {
				logResult.ResultStatus = globalevents.ResultSkipped
			}
			logResult.Content += "\nreport stopped after foreground takeover"
			return logResult, err
		}
		item := deliveries[index]
		if item.Status == storage.ElnisReportDeliveryDelivered {
			index++
			continue
		}
		var target Target
		if err := json.Unmarshal([]byte(item.Target), &target); err != nil {
			logResult.ResultStatus = globalevents.ResultFailed
			return s.failReportDelivery(ctx, eventID, item.ID, logResult, fmt.Errorf("decode elnis report target: %w", err))
		}
		batch := []storage.ElnisReportDelivery{item}
		index++
		for index < len(deliveries) {
			next := deliveries[index]
			if next.Status == storage.ElnisReportDeliveryDelivered || next.Target != item.Target {
				break
			}
			batch = append(batch, next)
			index++
		}
		outputs := make([]delivery.Output, 0, len(batch))
		for _, deliveryItem := range batch {
			var output delivery.Output
			if err := json.Unmarshal([]byte(deliveryItem.Output), &output); err != nil {
				logResult.ResultStatus = globalevents.ResultFailed
				return s.failReportDelivery(ctx, eventID, deliveryItem.ID, logResult, fmt.Errorf("decode elnis report output: %w", err))
			}
			if err := repo.StartReportDelivery(ctx, deliveryItem.ID); err != nil {
				if logResult.ResultStatus != globalevents.ResultFailed {
					logResult.ResultStatus = globalevents.ResultFailed
					if errors.Is(err, context.Canceled) {
						logResult.ResultStatus = globalevents.ResultCanceled
					}
				}
				return s.failReportDelivery(ctx, eventID, deliveryItem.ID, logResult, err)
			}
			outputs = append(outputs, output)
		}
		receipt, result, err := s.send(ctx, target.ToDeliveryTarget(), outputs)
		s.mapReportReceipt(ctx, event.EventKey, event.SessionID, batch[0].MessageID, receipt)
		logResult.Content += "\nsend:\n" + result.Content
		if result.ResultStatus == globalevents.ResultFailed ||
			(result.ResultStatus == globalevents.ResultCanceled && logResult.ResultStatus != globalevents.ResultFailed) {
			logResult.ResultStatus = result.ResultStatus
		}
		if err != nil {
			return s.failReportDelivery(ctx, eventID, batch[0].ID, logResult, err)
		}
		receiptJSON, err := json.Marshal(receipt)
		if err != nil {
			logResult.ResultStatus = globalevents.ResultFailed
			return s.failReportDelivery(ctx, eventID, batch[0].ID, logResult, fmt.Errorf("marshal elnis report receipt: %w", err))
		}
		for _, deliveryItem := range batch {
			if err := repo.MarkReportDeliveryDelivered(ctx, deliveryItem.ID, string(receiptJSON)); err != nil {
				if logResult.ResultStatus != globalevents.ResultFailed {
					logResult.ResultStatus = globalevents.ResultFailed
					if errors.Is(err, context.Canceled) {
						logResult.ResultStatus = globalevents.ResultCanceled
					}
				}
				return s.releaseReport(ctx, eventID, logResult, err)
			}
		}
	}
	if err := repo.CompleteReport(ctx, eventID, StatusDelivering, StatusCompleted); err != nil {
		if logResult.ResultStatus != globalevents.ResultFailed {
			logResult.ResultStatus = globalevents.ResultFailed
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
		}
		return s.releaseReport(ctx, eventID, logResult, err)
	}
	content, _ := json.Marshal(map[string]any{"event_id": eventID, "event_key": event.EventKey, "result": fmt.Sprintf("%.2048s", logResult.Content)})
	record := globalevents.LogRecord{Category: globalevents.LogAudit, Level: slog.LevelInfo, EventName: "elnis.llm_completed", Module: "elnis", Type: globalevents.ContentJSON, Content: string(content), ResultStatus: logResult.ResultStatus}
	if logResult.ResultStatus == globalevents.ResultFailed {
		record.Level = slog.LevelError
	}
	record.Identity = identity
	record.Identity.SessionID = &event.SessionID
	background.LogBackground(ctx, record)
	record.Category, record.EventName = globalevents.LogElnis, "elnis_llm_completed"
	background.LogBackground(ctx, record)
	return logResult, nil
}

func (s *Service) failReportDelivery(ctx context.Context, eventID, deliveryID string, logResult globalevents.LogResult, deliveryErr error) (globalevents.LogResult, error) {
	if err := s.store.ElnisEvents().MarkReportDeliveryFailed(ctx, eventID, deliveryID, StatusResultReady, deliveryErr.Error()); err != nil {
		logResult.Content += fmt.Sprintf("\npersistence_error=%v", err)
		if logResult.ResultStatus != globalevents.ResultFailed {
			logResult.ResultStatus = globalevents.ResultFailed
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
		}
		return logResult, fmt.Errorf("%v; persist delivery failure: %w", deliveryErr, err)
	}
	return logResult, deliveryErr
}

func (s *Service) releaseReport(ctx context.Context, eventID string, logResult globalevents.LogResult, deliveryErr error) (globalevents.LogResult, error) {
	if err := s.store.ElnisEvents().ReleaseReport(ctx, eventID, StatusDelivering, StatusResultReady, deliveryErr.Error()); err != nil {
		logResult.Content += fmt.Sprintf("\npersistence_error=%v", err)
		if logResult.ResultStatus != globalevents.ResultFailed {
			logResult.ResultStatus = globalevents.ResultFailed
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
		}
		return logResult, fmt.Errorf("%v; release report claim: %w", deliveryErr, err)
	}
	return logResult, deliveryErr
}

func (s *Service) resetDeliveringReports(ctx context.Context) error {
	return s.store.ElnisEvents().ResetDeliveringReports(ctx, StatusDelivering, StatusResultReady)
}

// recoverReports owns one recovery pass; each report retains its declared result.
func (s *Service) recoverReports(ctx context.Context, resetDelivering bool) (globalevents.LogResult, error) {
	repo := s.store.ElnisEvents()
	if resetDelivering {
		if err := s.resetDeliveringReports(ctx); err != nil {
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
			return logResult, err
		}
	}
	ids, err := repo.ListResultReadyReportIDs(ctx, StatusResultReady)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return logResult, err
	}
	var firstErr error
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSkipped}
	for _, eventID := range ids {
		result, err := s.deliverReport(ctx, eventID)
		logResult.Content += "\n" + result.Content
		if logResult.ResultStatus == globalevents.ResultSkipped || result.ResultStatus == globalevents.ResultFailed ||
			(result.ResultStatus == globalevents.ResultCanceled && logResult.ResultStatus != globalevents.ResultFailed) {
			logResult.ResultStatus = result.ResultStatus
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if result.ResultStatus == globalevents.ResultFailed || result.ResultStatus == globalevents.ResultCanceled {
			content, _ := json.Marshal(map[string]any{"event_id": eventID, "result": fmt.Sprintf("%.2048s", result.Content)})
			record := globalevents.LogRecord{Category: globalevents.LogElnis, Level: slog.LevelWarn, EventName: "recover_elnis_report_failed", Module: "elnis", Type: globalevents.ContentJSON, Content: string(content), ResultStatus: result.ResultStatus}
			if result.ResultStatus == globalevents.ResultCanceled {
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
			background.LogBackground(ctx, record)
		}
	}
	return logResult, firstErr
}
