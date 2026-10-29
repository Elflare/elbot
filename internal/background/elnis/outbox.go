package elnis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	background "elbot/internal/background/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/workspace"
)

func (s *Service) prepareReport(ctx context.Context, event Event, eventID, resultJSON, sessionID, messageID string, result background.JSONResult) (bool, error) {
	if taken, err := s.sessionTakenOver(ctx, sessionID); err != nil {
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
	dir, err := workspace.SandboxDir(s.sandboxRoot, elnisSandboxSubdir(event.Request.Elwisp.Name))
	if err != nil {
		return false, err
	}
	result.ReportSegments, err = s.importReportSegments(ctx, result.ReportSegments, eventID, dir)
	if err != nil {
		return false, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	resultJSON = string(encoded)

	outputs := make([]delivery.Output, 0, 1+len(result.ReportSegments))
	if text := strings.TrimSpace(result.Report); text != "" {
		outputs = append(outputs, delivery.Text(text))
	}
	outputs = append(outputs, result.ReportSegments...)
	if len(outputs) == 0 {
		return false, s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusCompleted, sessionID, resultJSON, "")
	}

	deliveries := make([]CreateReportDeliveryRequest, 0, len(resolved)*len(outputs))
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
			deliveries = append(deliveries, CreateReportDeliveryRequest{
				Target:    string(targetJSON),
				Output:    string(outputJSON),
				MessageID: messageID,
			})
		}
	}
	if err := s.store.PrepareReport(ctx, PrepareReportRequest{
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
	repo := s.store
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
		if taken, err := s.sessionTakenOver(ctx, event.SessionID); err != nil {
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
		if item.Status == ReportDeliveryDelivered {
			index++
			continue
		}
		var target Target
		if err := json.Unmarshal([]byte(item.Target), &target); err != nil {
			logResult.ResultStatus = globalevents.ResultFailed
			return s.failReportDelivery(ctx, eventID, item.ID, logResult, fmt.Errorf("decode elnis report target: %w", err), delivery.Receipt{}, nil)
		}
		batch := []ReportDelivery{item}
		index++
		for index < len(deliveries) {
			next := deliveries[index]
			if next.Status == ReportDeliveryDelivered || next.Target != item.Target {
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
				return s.failReportDelivery(ctx, eventID, deliveryItem.ID, logResult, fmt.Errorf("decode elnis report output: %w", err), delivery.Receipt{}, nil)
			}
			if err := repo.StartReportDelivery(ctx, deliveryItem.ID); err != nil {
				if logResult.ResultStatus != globalevents.ResultFailed {
					logResult.ResultStatus = globalevents.ResultFailed
					if errors.Is(err, context.Canceled) {
						logResult.ResultStatus = globalevents.ResultCanceled
					}
				}
				return s.failReportDelivery(ctx, eventID, deliveryItem.ID, logResult, err, delivery.Receipt{}, nil)
			}
			outputs = append(outputs, output)
		}
		receipt, result, err := s.send(ctx, toDeliveryTarget(target), outputs)
		mappings := background.MapReportReceipt(event.SessionID, batch[0].MessageID, receipt)
		logResult.Content += "\nsend:\n" + result.Content
		if result.ResultStatus == globalevents.ResultFailed ||
			(result.ResultStatus == globalevents.ResultCanceled && logResult.ResultStatus != globalevents.ResultFailed) {
			logResult.ResultStatus = result.ResultStatus
		}
		if err != nil {
			return s.failReportDelivery(ctx, eventID, batch[0].ID, logResult, err, receipt, mappings)
		}
		// Receipt contains only strings, integers and slices.
		receiptJSON, _ := json.Marshal(receipt)
		deliveryIDs := make([]string, 0, len(batch))
		for _, deliveryItem := range batch {
			deliveryIDs = append(deliveryIDs, deliveryItem.ID)
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = repo.MarkReportDeliveriesDelivered(cleanupCtx, deliveryIDs, string(receiptJSON), mappings)
		cancel()
		if err != nil {
			logResult.ResultStatus = globalevents.ResultFailed
			return s.releaseReport(ctx, eventID, logResult, err)
		}
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := repo.CompleteReport(cleanupCtx, eventID, StatusDelivering, StatusCompleted); err != nil {
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

func (s *Service) failReportDelivery(ctx context.Context, eventID, deliveryID string, logResult globalevents.LogResult, deliveryErr error, receipt delivery.Receipt, mappings []background.ReportMessageMap) (globalevents.LogResult, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	receiptJSON, _ := json.Marshal(receipt)
	if err := s.store.MarkReportDeliveryFailed(cleanupCtx, eventID, deliveryID, StatusResultReady, deliveryErr.Error(), string(receiptJSON), mappings); err != nil {
		logResult.Content += fmt.Sprintf("\npersistence_error=%v", err)
		if logResult.ResultStatus != globalevents.ResultFailed {
			logResult.ResultStatus = globalevents.ResultFailed
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
		}
		return logResult, errors.Join(deliveryErr, fmt.Errorf("persist delivery failure: %w", err))
	}
	return logResult, deliveryErr
}

func (s *Service) releaseReport(ctx context.Context, eventID string, logResult globalevents.LogResult, deliveryErr error) (globalevents.LogResult, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.store.ReleaseReport(cleanupCtx, eventID, StatusDelivering, StatusResultReady, deliveryErr.Error()); err != nil {
		logResult.Content += fmt.Sprintf("\npersistence_error=%v", err)
		if logResult.ResultStatus != globalevents.ResultFailed {
			logResult.ResultStatus = globalevents.ResultFailed
			if errors.Is(err, context.Canceled) {
				logResult.ResultStatus = globalevents.ResultCanceled
			}
		}
		return logResult, errors.Join(deliveryErr, fmt.Errorf("release report claim: %w", err))
	}
	return logResult, deliveryErr
}

func (s *Service) resetDeliveringReports(ctx context.Context) error {
	return s.store.ResetDeliveringReports(ctx, StatusDelivering, StatusResultReady)
}

// recoverReports owns one recovery pass; each report retains its declared result.
func (s *Service) recoverReports(ctx context.Context) (globalevents.LogResult, error) {
	repo := s.store
	ids, err := repo.ListResultReadyReportIDs(ctx, StatusResultReady)
	if err != nil {
		logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return logResult, err
	}
	var errs []error
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSkipped}
	for _, eventID := range ids {
		result, err := s.deliverReport(ctx, eventID)
		logResult.Content += "\n" + result.Content
		if logResult.ResultStatus == globalevents.ResultSkipped || result.ResultStatus == globalevents.ResultFailed ||
			(result.ResultStatus == globalevents.ResultCanceled && logResult.ResultStatus != globalevents.ResultFailed) {
			logResult.ResultStatus = result.ResultStatus
		}
		errs = append(errs, err)
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
	return logResult, errors.Join(errs...)
}
