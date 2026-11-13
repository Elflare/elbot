package elnis

import (
	"context"
	"elbot/internal/operationresult"
	"encoding/json"
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

func (s *Service) prepareReport(ctx context.Context, event Event, eventID, resultJSON, sessionID, messageID string, result background.JSONResult) (operationresult.Result[bool], error) {
	taken, err := s.sessionTakenOver(ctx, sessionID)
	if err != nil {
		return operationresult.Result[bool]{}, err
	} else if taken {
		return operationresult.Result[bool]{Status: operationresult.Success}, s.completeTakeover(ctx, eventID, event.ResolvedTargets, background.RunResult{SessionID: sessionID, MessageID: messageID, TakenOver: true, Outcome: "taken_over", Text: resultJSON}, nil)
	}
	resolved, err := decodeResolvedTargets(event.ResolvedTargets)
	if err != nil {
		return operationresult.Result[bool]{}, err
	}
	dir, err := workspace.SandboxDir(s.sandboxRoot, elnisSandboxSubdir(event.Request.Elwisp.Name))
	if err != nil {
		return operationresult.Result[bool]{}, err
	}
	segments, importErr := s.importReportSegments(ctx, result.ReportSegments, eventID, dir)
	if importErr != nil {
		return operationresult.Result[bool]{}, importErr
	}
	if segments.Status != operationresult.Success {
		return operationresult.Result[bool]{Status: segments.Status, Message: segments.Message}, nil
	}
	result.ReportSegments = segments.Data
	encoded, _ := json.Marshal(result)
	resultJSON = string(encoded)

	outputs := make([]delivery.Output, 0, 1+len(result.ReportSegments))
	if text := strings.TrimSpace(result.Report); text != "" {
		outputs = append(outputs, delivery.Text(text))
	}
	outputs = append(outputs, result.ReportSegments...)
	if len(outputs) == 0 {
		return operationresult.Result[bool]{Status: operationresult.Success}, s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusCompleted, sessionID, resultJSON, "")
	}

	deliveries := make([]CreateReportDeliveryRequest, 0, len(resolved)*len(outputs))
	for _, target := range resolved {
		targetJSON, _ := json.Marshal(target)
		for _, output := range outputs {
			outputJSON, _ := json.Marshal(output)
			deliveries = append(deliveries, CreateReportDeliveryRequest{
				Target:    string(targetJSON),
				Output:    string(outputJSON),
				MessageID: messageID,
			})
		}
	}
	if err = s.store.PrepareReport(ctx, PrepareReportRequest{
		EventID:           eventID,
		ResolvedTargets:   event.ResolvedTargets,
		SessionID:         sessionID,
		Result:            resultJSON,
		Deliveries:        deliveries,
		ResultReadyStatus: StatusResultReady,
	}); err != nil {
		return operationresult.Result[bool]{}, err
	}
	return operationresult.Result[bool]{Status: operationresult.Success, Data: true}, nil
}

// deliverReport returns terminal failures to the task or recovery entrypoint.
func (s *Service) deliverReport(ctx context.Context, eventID string) (context.Context, operationresult.Result[struct{}], error) {
	var err error
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	identity := globalevents.LogIdentityFromContext(ctx)
	repo := s.store
	claimed, err := repo.ClaimReport(ctx, eventID, StatusResultReady, StatusDelivering)
	if err != nil {
		return ctx, operationresult.Result[struct{}]{}, err
	}
	if !claimed {
		return ctx, operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}
	event, err := repo.Get(ctx, eventID)
	if err != nil {
		return ctx, operationresult.Result[struct{}]{}, s.releaseReport(ctx, eventID, err)
	}
	identity.SessionID = event.SessionID
	facts.SessionID = event.SessionID
	ctx = contextinfo.WithExecution(ctx, facts)
	deliveries, err := repo.ListReportDeliveries(ctx, eventID)
	if err != nil {
		return ctx, operationresult.Result[struct{}]{}, s.releaseReport(ctx, eventID, err)
	}

	for index := 0; index < len(deliveries); {
		var taken bool
		taken, err = s.sessionTakenOver(ctx, event.SessionID)
		if err != nil {
			return ctx, operationresult.Result[struct{}]{}, s.releaseReport(ctx, eventID, err)
		} else if taken {
			return ctx, operationresult.Result[struct{}]{Status: operationresult.Success}, s.completeTakeover(ctx, eventID, event.ResolvedTargets, background.RunResult{SessionID: event.SessionID, TakenOver: true, Outcome: "taken_over", Text: event.Result}, nil)
		}
		item := deliveries[index]
		if item.Status == ReportDeliveryDelivered {
			index++
			continue
		}
		var target Target
		if err = json.Unmarshal([]byte(item.Target), &target); err != nil {
			failure, fault := s.failReportDelivery(ctx, eventID, item.ID, operationresult.Result[struct{}]{}, fmt.Errorf("decode elnis report target: %w", err))
			return ctx, failure, fault
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
			if err = json.Unmarshal([]byte(deliveryItem.Output), &output); err != nil {
				failure, fault := s.failReportDelivery(ctx, eventID, deliveryItem.ID, operationresult.Result[struct{}]{}, fmt.Errorf("decode elnis report output: %w", err))
				return ctx, failure, fault
			}
			if err = repo.StartReportDelivery(ctx, deliveryItem.ID); err != nil {
				failure, fault := s.failReportDelivery(ctx, eventID, deliveryItem.ID, operationresult.Result[struct{}]{}, err)
				return ctx, failure, fault
			}
			outputs = append(outputs, output)
		}
		sent, sendErr := s.send(ctx, toDeliveryTarget(target), outputs)
		if sendErr != nil || sent.Status != operationresult.Success {
			failure, fault := s.failReportDelivery(ctx, eventID, batch[0].ID, operationresult.Result[struct{}]{Status: sent.Status, Message: sent.Message}, sendErr)
			return ctx, failure, fault
		}
		receipt := sent.Data
		mappings := background.MapReportReceipt(event.SessionID, batch[0].MessageID, receipt)
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
			return ctx, operationresult.Result[struct{}]{}, s.releaseReport(ctx, eventID, err)
		}
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = repo.CompleteReport(cleanupCtx, eventID, StatusDelivering, StatusCompleted); err != nil {
		return ctx, operationresult.Result[struct{}]{}, s.releaseReport(ctx, eventID, err)
	}
	content, _ := json.Marshal(map[string]any{"event_id": eventID, "event_key": event.EventKey, "result": "report delivered"})
	record := globalevents.LogRecord{Category: globalevents.LogCategoryAudit, Level: slog.LevelInfo, EventName: "elnis.llm_completed", Module: "elnis", Type: globalevents.ContentJSON, Content: string(content), ResultStatus: globalevents.ResultSucceeded}
	record.Identity = identity
	record.Identity.SessionID = event.SessionID
	globalevents.Log(ctx, record)
	record.Category, record.EventName = globalevents.LogCategoryElnis, "elnis_llm_completed"
	globalevents.Log(ctx, record)
	return ctx, operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (s *Service) failReportDelivery(ctx context.Context, eventID, deliveryID string, result operationresult.Result[struct{}], deliveryErr error) (operationresult.Result[struct{}], error) {
	message := result.Message
	if deliveryErr != nil {
		message = deliveryErr.Error()
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.store.MarkReportDeliveryFailed(cleanupCtx, eventID, deliveryID, StatusResultReady, message, "{}", nil); err != nil {
		globalevents.Log(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryElnis, Level: slog.LevelError, Module: "elnis", EventName: "elnis_report_failure_save_failed", ResultStatus: globalevents.LogStatus(err), Identity: globalevents.LogIdentityFromContext(ctx), Content: err.Error()})
		if deliveryErr != nil {
			return operationresult.Result[struct{}]{}, deliveryErr
		}
		return operationresult.Result[struct{}]{}, err
	}
	return result, deliveryErr
}

func (s *Service) releaseReport(ctx context.Context, eventID string, deliveryErr error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.store.ReleaseReport(cleanupCtx, eventID, StatusDelivering, StatusResultReady, deliveryErr.Error()); err != nil {
		record := globalevents.LogRecord{Category: globalevents.LogCategoryElnis, Level: slog.LevelError, Module: "elnis", EventName: "elnis_report_release_failed", ResultStatus: globalevents.LogStatus(err), Identity: globalevents.LogIdentityFromContext(ctx), Content: fmt.Sprintf("event_id=%s error=%v", eventID, err)}
		if record.ResultStatus == globalevents.ResultCanceled {
			record.Level = slog.LevelInfo
		}
		globalevents.Log(ctx, record)
	}
	return deliveryErr
}

func (s *Service) resetDeliveringReports(ctx context.Context) error {
	return s.store.ResetDeliveringReports(ctx, StatusDelivering, StatusResultReady)
}

// Recovery handles each failed report and continues with independent reports.
func (s *Service) recoverReports(ctx context.Context) {
	repo := s.store
	ids, err := repo.ListResultReadyReportIDs(ctx, StatusResultReady)
	if err != nil {
		record := globalevents.LogRecord{Category: globalevents.LogCategoryElnis, Level: slog.LevelError, Module: "elnis", EventName: "recover_elnis_report_failed", ResultStatus: globalevents.LogStatus(err), Identity: globalevents.LogIdentityFromContext(ctx), Content: "list pending reports: " + err.Error()}
		if record.ResultStatus == globalevents.ResultCanceled {
			record.Level = slog.LevelInfo
		}
		globalevents.Log(ctx, record)
		return
	}

	for _, eventID := range ids {
		ctx, result, err := s.deliverReport(ctx, eventID)
		if err != nil {
			record := globalevents.LogRecord{Category: globalevents.LogCategoryElnis, Level: slog.LevelWarn, EventName: "recover_elnis_report_failed", Module: "elnis", Content: err.Error(), Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.LogStatus(err)}
			if record.ResultStatus == globalevents.ResultCanceled {
				record.Level = slog.LevelInfo
			}
			globalevents.Log(ctx, record)
		} else if result.Status != operationresult.Success {
			globalevents.Log(ctx, globalevents.LogRecord{Category: globalevents.LogCategoryElnis, Level: slog.LevelWarn, EventName: "recover_elnis_report_rejected", Module: "elnis", Content: result.Message, Identity: globalevents.LogIdentityFromContext(ctx), ResultStatus: globalevents.ResultFailed})
		}
	}
}
