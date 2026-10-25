package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"elbot/internal/background"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/workspace"
)

type resolvedCronTarget struct {
	key      string
	platform string
	target   delivery.Target
}

func (s *Service) deliverPrepared(ctx context.Context, job storage.CronJob, meta Metadata, state CronDeliveryState, platformFilter string, recovery bool) (logResult globalevents.LogResult, err error) {
	logResult = globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "operation=deliverPrepared"}
	defer func() {
		if err != nil {
			logResult.Content += fmt.Sprintf("\nerror=%v", err)
		}
	}()
	if !state.ReportReady {
		return logResult, nil
	}
	taken, err := background.SessionTakenOver(ctx, s.store, state.ReportSessionID)
	if err != nil {
		logResult.ResultStatus = globalevents.ResultFailed
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		return logResult, err
	}
	state.TakenOver = state.TakenOver || taken
	if state.TakenOver {
		state.Report, state.ReportSegments = "", nil
		if _, err := s.persistDeliveryState(ctx, &job, state, nil); err != nil {
			logResult.ResultStatus = globalevents.ResultFailed
			return logResult, err
		}
		if meta.Schedule.Mode == ScheduleOnce {
			return s.disableCompletedDelivery(ctx, job.Name, job.DeliveryToken)
		}
		return logResult, nil
	}
	for _, resolved := range s.resolveDeliveryTargets(meta, job.Name) {
		if platformFilter != "" && resolved.platform != platformFilter {
			continue
		}
		targetState := ensureDeliveryTargetState(&state, resolved.key)
		for index, outputID := range deliveryOutputIDs(state) {
			taken, err := background.SessionTakenOver(ctx, s.store, state.ReportSessionID)
			if err != nil {
				if logResult.ResultStatus != globalevents.ResultFailed {
					logResult.ResultStatus = globalevents.ResultFailed
					if errors.Is(err, context.Canceled) {
						logResult.ResultStatus = globalevents.ResultCanceled
					}
				}
				return logResult, err
			}
			if taken {
				state.TakenOver = true
				result, err := s.deliverPrepared(ctx, job, meta, state, platformFilter, recovery)
				logResult.Content += "\n" + result.Content
				if result.ResultStatus == globalevents.ResultFailed || logResult.ResultStatus != globalevents.ResultFailed {
					logResult.ResultStatus = result.ResultStatus
				}
				return logResult, err
			}
			outputState := ensureDeliveryOutputState(targetState, outputID)
			if deliveryStatusDone(outputState.Status) {
				continue
			}
			var out delivery.Output
			var buildErr error
			status := DeliveryDelivered
			segmentIndex := index
			if strings.TrimSpace(state.Report) != "" {
				segmentIndex--
			}
			switch {
			case outputID == "text":
				text := strings.TrimSpace(state.Report)
				if recovery {
					text = missedOnceReportText(meta.Title, text)
				}
				out = delivery.Text(text)
			case outputState.Status == DeliveryFallbackPending:
				out, status = delivery.Text(outputState.FallbackText), DeliveryFallbackDelivered
			default:
				segment := state.ReportSegments[segmentIndex]
				var scope workspace.PathScope
				if segment.MediaID == "" && !delivery.IsHTTPMediaSource(segment.URL) {
					scope, buildErr = s.cronSandbox(job.Name)
				}
				if buildErr == nil {
					out, buildErr = background.BuildReportSegmentOutput(segment, scope)
				}
				if buildErr != nil && (!recovery || ctx.Err() != nil) {
					logResult.ResultStatus = globalevents.ResultFailed
				}
			}
			var receipt delivery.Receipt
			if buildErr == nil {
				var result globalevents.LogResult
				receipt, result, buildErr = s.sendDeliveryOutput(ctx, job.Name, resolved, out, state)
				logResult.Content += "\n" + result.Content
				if result.ResultStatus == globalevents.ResultFailed || (result.ResultStatus == globalevents.ResultCanceled && logResult.ResultStatus != globalevents.ResultFailed) {
					logResult.ResultStatus = result.ResultStatus
				}
			}
			if buildErr != nil && outputID != "text" && status != DeliveryFallbackDelivered && recovery && ctx.Err() == nil {
				logResult.Content += fmt.Sprintf("\nattachment_error=%v", buildErr)
				outputState.Status = DeliveryFallbackPending
				outputState.FallbackText = reportSegmentFallbackText(state.ReportSegments[segmentIndex])
				active, saveErr := s.persistDeliveryState(ctx, &job, state, s.mapReportReceipt(state.ReportSessionID, state.ReportMessageID, receipt))
				if saveErr != nil {
					logResult.ResultStatus = globalevents.ResultFailed
					return logResult, errors.Join(buildErr, saveErr)
				}
				if !active {
					return logResult, buildErr
				}
				var result globalevents.LogResult
				receipt, result, buildErr = s.sendDeliveryOutput(ctx, job.Name, resolved, delivery.Text(outputState.FallbackText), state)
				logResult.Content += "\n" + result.Content
				if result.ResultStatus == globalevents.ResultFailed || (result.ResultStatus == globalevents.ResultCanceled && logResult.ResultStatus != globalevents.ResultFailed) {
					logResult.ResultStatus = result.ResultStatus
				}
				status = DeliveryFallbackDelivered
			}
			if buildErr == nil {
				outputState.Status = status
			}
			if buildErr == nil || len(receipt.SentMessages) > 0 {
				active, saveErr := s.persistDeliveryState(ctx, &job, state, s.mapReportReceipt(state.ReportSessionID, state.ReportMessageID, receipt))
				if saveErr != nil {
					logResult.ResultStatus = globalevents.ResultFailed
					return logResult, errors.Join(buildErr, saveErr)
				}
				if !active {
					return logResult, buildErr
				}
			}
			if buildErr != nil {
				return logResult, buildErr
			}
		}
	}
	if meta.Schedule.Mode == ScheduleOnce && s.deliveryComplete(meta, state) {
		result, err := s.disableCompletedDelivery(ctx, job.Name, job.DeliveryToken)
		logResult.Content += "\n" + result.Content
		if result.ResultStatus == globalevents.ResultFailed || (result.ResultStatus == globalevents.ResultCanceled && logResult.ResultStatus != globalevents.ResultFailed) {
			logResult.ResultStatus = result.ResultStatus
		}
		return logResult, err
	}
	return logResult, nil
}

func (s *Service) sendDeliveryOutput(ctx context.Context, jobName string, resolved resolvedCronTarget, out delivery.Output, state CronDeliveryState) (delivery.Receipt, globalevents.LogResult, error) {
	return s.sendOutputsToPlatformTarget(ctx, jobName, resolved.platform, resolved.target, []delivery.Output{out}, state.ReportSessionID)
}

func (s *Service) persistDeliveryState(ctx context.Context, job *storage.CronJob, state CronDeliveryState, mappings []storage.PlatformMessageMap) (bool, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	ctx = cleanupCtx
	encoded, err := encodeDeliveryState(state)
	if err != nil {
		return false, err
	}
	swapped, err := s.store.CronJobs().CompareAndSwapDelivery(ctx, job.ID, job.DeliveryToken, job.DeliveryToken, encoded, mappings)
	if err != nil || !swapped {
		return swapped, err
	}
	job.DeliveryState = encoded
	return true, nil
}

func (s *Service) disableCompletedDelivery(ctx context.Context, jobName, deliveryToken string) (globalevents.LogResult, error) {
	_, err := s.manager.DisableJobIfDeliveryToken(ctx, jobName, deliveryToken)
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "operation=disableCompletedDelivery"}
	if err != nil {
		logResult.ResultStatus = globalevents.ResultFailed
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
		logResult.Content += fmt.Sprintf("\nerror=%v", err)
	}
	return logResult, err
}

func (s *Service) resolveDeliveryTargets(meta Metadata, jobName string) []resolvedCronTarget {
	platforms := s.enabledPlatforms
	if !meta.Target.AllEnabledPlatforms {
		platforms = []PlatformTarget{{Name: meta.Target.SourcePlatform}}
		if id := strings.TrimSpace(meta.CreatedBy.PlatformUserID); id != "" {
			platforms[0].SuperadminIDs = []string{id}
		}
	}
	resolved := []resolvedCronTarget{}
	for _, platform := range platforms {
		platformName := platform.Name
		ids := uniqueStrings(platform.SuperadminIDs)
		if len(ids) == 0 {
			resolved = append(resolved, resolvedCronTarget{key: platformName + "|superadmins", platform: platformName, target: delivery.Target{Platform: platformName, Superadmins: true}})
			continue
		}
		for _, id := range ids {
			resolved = append(resolved, resolvedCronTarget{key: platformName + "|private|" + id, platform: platformName, target: delivery.Target{Platform: platformName, PrivateUserID: id}})
		}
	}
	return resolved
}

func (s *Service) deliveryComplete(meta Metadata, state CronDeliveryState) bool {
	if state.TakenOver {
		return true
	}
	if !state.ReportReady {
		return false
	}
	outputIDs := deliveryOutputIDs(state)
	for _, target := range s.resolveDeliveryTargets(meta, "") {
		targetState := findDeliveryTargetState(state, target.key)
		for _, outputID := range outputIDs {
			if targetState == nil || !deliveryStatusDone(findDeliveryOutputStatus(*targetState, outputID)) {
				return false
			}
		}
	}
	return true
}

func deliveryOutputIDs(state CronDeliveryState) []string {
	ids := []string{}
	if strings.TrimSpace(state.Report) != "" {
		ids = append(ids, "text")
	}
	for index := range state.ReportSegments {
		ids = append(ids, fmt.Sprintf("segment:%d", index))
	}
	return ids
}

func ensureDeliveryTargetState(state *CronDeliveryState, key string) *CronDeliveryTargetState {
	for index := range state.Targets {
		if state.Targets[index].Key == key {
			return &state.Targets[index]
		}
	}
	state.Targets = append(state.Targets, CronDeliveryTargetState{Key: key})
	return &state.Targets[len(state.Targets)-1]
}

func ensureDeliveryOutputState(state *CronDeliveryTargetState, id string) *CronDeliveryOutputState {
	for index := range state.Outputs {
		if state.Outputs[index].ID == id {
			return &state.Outputs[index]
		}
	}
	state.Outputs = append(state.Outputs, CronDeliveryOutputState{ID: id, Status: DeliveryPending})
	return &state.Outputs[len(state.Outputs)-1]
}

func findDeliveryTargetState(state CronDeliveryState, key string) *CronDeliveryTargetState {
	for index := range state.Targets {
		if state.Targets[index].Key == key {
			return &state.Targets[index]
		}
	}
	return nil
}

func findDeliveryOutputStatus(state CronDeliveryTargetState, id string) DeliveryStatus {
	for _, output := range state.Outputs {
		if output.ID == id {
			return output.Status
		}
	}
	return DeliveryPending
}

func deliveryStatusDone(status DeliveryStatus) bool {
	return status == DeliveryDelivered || status == DeliveryFallbackDelivered
}

func reportSegmentFallbackText(segment llm.MessageSegment) string {
	value := strings.TrimSpace(segment.URL)
	if delivery.IsHTTPMediaSource(value) {
		return fmt.Sprintf("url %s 发送失败", value)
	}
	return fmt.Sprintf("路径 %s 附件发送失败", value)
}

func (s *Service) cronSandbox(jobName string) (workspace.PathScope, error) {
	dir, err := workspace.SandboxDir(s.sandboxRoot, cronSandboxSubdir(jobName))
	return workspace.PathScope{Dir: dir, Restricted: true}, err
}

func encodeDeliveryState(state CronDeliveryState) (string, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeDeliveryState(raw string) (CronDeliveryState, error) {
	if strings.TrimSpace(raw) == "" {
		return CronDeliveryState{}, nil
	}
	var state CronDeliveryState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return CronDeliveryState{}, fmt.Errorf("decode cron delivery state: %w", err)
	}
	return state, nil
}

func (s *Service) lockDeliveryJob(ctx context.Context, jobName string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	jobName = normalizeJobName(jobName)
	s.mu.Lock()
	gate := s.deliveryGates[jobName]
	if gate == nil {
		gate = make(chan struct{}, 1)
		s.deliveryGates[jobName] = gate
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return nil, err
		}
		return func() { <-gate }, nil
	}
}

func (s *Service) sendToPlatforms(ctx context.Context, jobName string, platforms []string, text string) (globalevents.LogResult, error) {
	return s.sendOutputsToPlatforms(ctx, jobName, platforms, []delivery.Output{delivery.Text(text)})
}

func (s *Service) sendOutputsToPlatformTarget(ctx context.Context, jobName, platformName string, target delivery.Target, outputs []delivery.Output, sessionID string) (delivery.Receipt, globalevents.LogResult, error) {
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

	content := fmt.Sprintf("platform=%s target=%q", platformName, cronTargetLabel(target))
	if sessionID != "" {
		content += " session_id=" + sessionID
	}
	resultStatus := globalevents.ResultSucceeded
	var errs []error
	var receipt delivery.Receipt
	for _, out := range outputs {
		if err := ctx.Err(); err != nil {
			if resultStatus != globalevents.ResultFailed {
				resultStatus = globalevents.ResultFailed
				if err == context.Canceled {
					resultStatus = globalevents.ResultCanceled
				}
			}
			{
				err := errors.Join(append(errs, err)...)
				logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content + fmt.Sprintf("\ncontext_error=%q", ctx.Err().Error())}
				return receipt, logResult, err
			}
		}
		{
			content, _ := json.Marshal(map[string]any{"job": jobName, "platform": platformName, "target": cronTargetLabel(target), "kind": out.Kind})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelInfo,
				EventName:    "cron.send_started",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
			}
			record.Identity = identity
			if sessionID != "" {
				record.Identity.SessionID = &sessionID
			}
			background.LogBackground(ctx, record)
		}
		sent, logResult, err := s.sendTarget(ctx, target, []delivery.Output{out})
		content += fmt.Sprintf("\nkind=%s result=%s", out.Kind, logResult.ResultStatus.String())
		if logResult.Content != "" {
			content += "\n" + logResult.Content
		}
		if logResult.ResultStatus == globalevents.ResultFailed || (logResult.ResultStatus == globalevents.ResultCanceled && resultStatus != globalevents.ResultFailed) {
			resultStatus = logResult.ResultStatus
		}
		receipt.PlatformMessageIDs = append(receipt.PlatformMessageIDs, sent.PlatformMessageIDs...)
		receipt.SentMessages = append(receipt.SentMessages, sent.SentMessages...)
		if err != nil {
			err = fmt.Errorf("send %s: %w", platformName, err)
			errs = append(errs, err)
		}
		if logResult.ResultStatus == globalevents.ResultFailed || logResult.ResultStatus == globalevents.ResultCanceled {
			status := logResult.ResultStatus
			{
				content, _ := json.Marshal(map[string]any{"job": jobName, "platform": platformName, "target": cronTargetLabel(target), "kind": out.Kind, "content": logResult.Content})
				record := globalevents.LogRecord{
					Category:     globalevents.LogAudit,
					Level:        slog.LevelError,
					EventName:    "cron.send_failed",
					Module:       "cron",
					Type:         globalevents.ContentJSON,
					Content:      string(content),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				record.ResultStatus = status
				if status == globalevents.ResultCanceled {
					record.Level = slog.LevelInfo
				}

				record.Identity = identity
				if sessionID != "" {
					record.Identity.SessionID = &sessionID
				}
				background.LogBackground(ctx, record)
			}
			if err != nil && ctx.Err() != nil {
				{
					err := errors.Join(errs...)
					logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
					return receipt, logResult, err
				}
			}
			continue
		}
		{
			content, _ := json.Marshal(map[string]any{"job": jobName, "platform": platformName, "target": cronTargetLabel(target), "kind": out.Kind, "content": logResult.Content})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelInfo,
				EventName:    "cron.send_completed",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
				ResultStatus: logResult.ResultStatus,
			}
			record.Identity = identity
			if sessionID != "" {
				record.Identity.SessionID = &sessionID
			}
			background.LogBackground(ctx, record)
		}
	}
	{
		err := errors.Join(errs...)
		logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
		return receipt, logResult, err
	}
}

func (s *Service) sendOutputsToPlatforms(ctx context.Context, jobName string, platforms []string, outputs []delivery.Output) (globalevents.LogResult, error) {
	content := "operation=sendOutputsToPlatforms"

	resultStatus := globalevents.ResultSucceeded
	var errs []error
	for _, platformName := range uniqueStrings(platforms) {
		if platformName == "" {
			continue
		}
		_, logResult, err := s.sendOutputsToPlatformTarget(ctx, jobName, platformName, delivery.Target{Platform: platformName, Superadmins: true}, outputs, "")
		status := logResult.ResultStatus
		content += "\n" + logResult.Content
		if status == globalevents.ResultFailed || resultStatus == globalevents.ResultFailed {
			resultStatus = globalevents.ResultFailed
		} else if status == globalevents.ResultCanceled {
			resultStatus = globalevents.ResultCanceled
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			{
				err := errors.Join(errs...)
				logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
				return logResult, err
			}
		}
	}
	{
		err := errors.Join(errs...)
		logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
		return logResult, err
	}
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

func cronTargetLabel(target delivery.Target) string {
	if target.Superadmins {
		return "superadmins"
	}
	if target.PrivateUserID != "" {
		return "private"
	}
	if target.GroupID != "" {
		return "group"
	}
	return "unknown"
}

func (s *Service) copySessionToBroadcastTargets(ctx context.Context, sourceSessionID string, meta Metadata, jobName string) error {
	source, err := s.store.Sessions().Get(ctx, sourceSessionID)
	if err != nil {
		return err
	}
	for _, target := range s.enabledPlatforms {
		if target.Name == source.Platform {
			continue
		}
		owner := targetOwnerID(target)
		if owner == "" {
			continue
		}
		metadata := storage.SessionMetadata{}
		for key, value := range map[string]any{"cron_job_name": jobName, "cron_source_session_id": sourceSessionID, "cron_broadcast_copy": true} {
			if err := metadata.Set(key, value); err != nil {
				return err
			}
		}
		copySession, err := s.sessions.CopyBackground(ctx, session.Scope{ActorID: owner, Platform: target.Name, PlatformScopeID: cronScopeID(jobName)}, session.BackgroundCopyRequest{SourceSessionID: sourceSessionID, Kind: "cron", Name: jobName, Title: meta.Title, Metadata: metadata})
		if err != nil {
			return err
		}
		{
			content, _ := json.Marshal(map[string]any{"job": jobName, "source_session_id": sourceSessionID, "target_session_id": copySession.ID, "platform": target.Name})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelInfo,
				EventName:    "cron.session_copied",
				Module:       "cron",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSucceeded,
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
	return nil
}
