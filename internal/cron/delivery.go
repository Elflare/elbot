package cron

import (
	"context"
	"elbot/internal/contextinfo"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"elbot/internal/background"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type resolvedCronTarget struct {
	key      string
	platform string
	target   delivery.Target
}

func (s *Service) deliverPrepared(ctx context.Context, job storage.CronJob, meta Metadata, state CronDeliveryState, platformFilter string, recovery bool) (logResult globalevents.LogResult, err error) {
	content := "operation=deliverPrepared"
	resultStatus := globalevents.ResultSucceeded
	defer func() {
		if resultStatus == globalevents.ResultFailed || (resultStatus == globalevents.ResultCanceled && logResult.ResultStatus != globalevents.ResultFailed) {
			logResult.ResultStatus = resultStatus
		}
	}()
	if !state.ReportReady {
		return globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: content}, nil
	}
	if taken, err := background.SessionTakenOver(ctx, s.store, state.ReportSessionID); err != nil {
		resultStatus := globalevents.ResultFailed
		if err == context.Canceled {
			resultStatus = globalevents.ResultCanceled
		}
		{
			err := err
			logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
			if err != nil {
				logResult.Content += fmt.Sprintf("\nerror=%v", err)
			}
			return logResult, err
		}
	} else if taken {
		state.TakenOver = true
	}
	if state.TakenOver {
		state.Report = ""
		state.ReportSegments = nil
		_, err := s.persistDeliveryState(ctx, &job, state)
		if err != nil {
			resultStatus := globalevents.ResultFailed
			if err == context.Canceled {
				resultStatus = globalevents.ResultCanceled
			}
			{
				err := err
				logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
				if err != nil {
					logResult.Content += fmt.Sprintf("\nerror=%v", err)
				}
				return logResult, err
			}
		}
		if meta.Schedule.Mode == ScheduleOnce {
			logResult, err := s.disableCompletedDelivery(ctx, job.Name, job.DeliveryToken)
			logResult.Content = content + "\n" + logResult.Content
			return logResult, err
		}
		return globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: content}, nil
	}
	targets := s.resolveDeliveryTargets(meta, job.Name)
	for _, resolved := range targets {
		if taken, err := background.SessionTakenOver(ctx, s.store, state.ReportSessionID); err != nil {
			resultStatus := globalevents.ResultFailed
			if err == context.Canceled {
				resultStatus = globalevents.ResultCanceled
			}
			{
				err := err
				logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
				if err != nil {
					logResult.Content += fmt.Sprintf("\nerror=%v", err)
				}
				return logResult, err
			}
		} else if taken {
			state.TakenOver = true
			return s.deliverPrepared(ctx, job, meta, state, platformFilter, recovery)
		}
		if platformFilter != "" && resolved.platform != platformFilter {
			continue
		}
		targetState := ensureDeliveryTargetState(&state, resolved.key)
		if report := strings.TrimSpace(state.Report); report != "" {
			outputState := ensureDeliveryOutputState(targetState, "text")
			if !deliveryStatusDone(outputState.Status) {
				text := report
				if recovery {
					text = missedOnceReportText(meta.Title, report)
				}
				logResult, err := s.sendDeliveryOutput(ctx, job.Name, resolved, delivery.Text(text), state)
				content += "\n" + logResult.Content
				if logResult.ResultStatus == globalevents.ResultFailed || (logResult.ResultStatus == globalevents.ResultCanceled && resultStatus != globalevents.ResultFailed) {
					resultStatus = logResult.ResultStatus
				}
				if err != nil {
					return globalevents.LogResult{ResultStatus: resultStatus, Content: content}, err
				}
				outputState.Status = DeliveryDelivered
				active, err := s.persistDeliveryState(ctx, &job, state)
				if err != nil || !active {
					{
						resultStatus := globalevents.ResultFailed
						if err == nil {
							resultStatus = globalevents.ResultSucceeded
						} else if err == context.Canceled {
							resultStatus = globalevents.ResultCanceled
						}
						{
							err := err
							logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
							if err != nil {
								logResult.Content += fmt.Sprintf("\nerror=%v", err)
							}
							return logResult, err
						}
					}
				}
			}
		}
		for index, segment := range state.ReportSegments {
			if taken, err := background.SessionTakenOver(ctx, s.store, state.ReportSessionID); err != nil {
				resultStatus := globalevents.ResultFailed
				if err == context.Canceled {
					resultStatus = globalevents.ResultCanceled
				}
				{
					err := err
					logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
					if err != nil {
						logResult.Content += fmt.Sprintf("\nerror=%v", err)
					}
					return logResult, err
				}
			} else if taken {
				state.TakenOver = true
				return s.deliverPrepared(ctx, job, meta, state, platformFilter, recovery)
			}
			outputState := ensureDeliveryOutputState(targetState, fmt.Sprintf("segment:%d", index))
			if deliveryStatusDone(outputState.Status) {
				continue
			}
			if outputState.Status == DeliveryFallbackPending {
				logResult, err := s.sendDeliveryOutput(ctx, job.Name, resolved, delivery.Text(outputState.FallbackText), state)
				content += "\n" + logResult.Content
				if logResult.ResultStatus == globalevents.ResultFailed || (logResult.ResultStatus == globalevents.ResultCanceled && resultStatus != globalevents.ResultFailed) {
					resultStatus = logResult.ResultStatus
				}
				if err != nil {
					return globalevents.LogResult{ResultStatus: resultStatus, Content: content}, err
				}
				outputState.Status = DeliveryFallbackDelivered
				active, err := s.persistDeliveryState(ctx, &job, state)
				if err != nil || !active {
					{
						resultStatus := globalevents.ResultFailed
						if err == nil {
							resultStatus = globalevents.ResultSucceeded
						} else if err == context.Canceled {
							resultStatus = globalevents.ResultCanceled
						}
						{
							err := err
							logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
							if err != nil {
								logResult.Content += fmt.Sprintf("\nerror=%v", err)
							}
							return logResult, err
						}
					}
				}
				continue
			}

			status := globalevents.ResultFailed
			out, buildErr := background.BuildReportSegmentOutput(segment, s.cronSandbox(job.Name))
			if buildErr == nil {
				logResult, buildErr = s.sendDeliveryOutput(ctx, job.Name, resolved, out, state)
				status = logResult.ResultStatus
				content += "\n" + logResult.Content
				if status == globalevents.ResultFailed || (status == globalevents.ResultCanceled && resultStatus != globalevents.ResultFailed) {
					resultStatus = status
				}
			}
			if buildErr == nil {
				outputState.Status = DeliveryDelivered
				active, err := s.persistDeliveryState(ctx, &job, state)
				if err != nil || !active {
					{
						resultStatus := globalevents.ResultFailed
						if err == nil {
							resultStatus = globalevents.ResultSucceeded
						} else if err == context.Canceled {
							resultStatus = globalevents.ResultCanceled
						}
						{
							err := err
							logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
							if err != nil {
								logResult.Content += fmt.Sprintf("\nerror=%v", err)
							}
							return logResult, err
						}
					}
				}
				continue
			}
			if !recovery || ctx.Err() != nil {
				{
					err := buildErr
					logResult := globalevents.LogResult{ResultStatus: status, Content: content}
					if err != nil {
						logResult.Content += fmt.Sprintf("\nerror=%v", err)
					}
					return logResult, err
				}
			}

			outputState.Status = DeliveryFallbackPending
			outputState.FallbackText = reportSegmentFallbackText(segment)
			active, err := s.persistDeliveryState(ctx, &job, state)
			if err != nil || !active {
				{
					err := errors.Join(buildErr, err)
					logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content}
					if err != nil {
						logResult.Content += fmt.Sprintf("\nerror=%v", err)
					}
					return logResult, err
				}
			}
			logResult, err = s.sendDeliveryOutput(ctx, job.Name, resolved, delivery.Text(outputState.FallbackText), state)
			content += "\n" + logResult.Content
			if logResult.ResultStatus == globalevents.ResultFailed || (logResult.ResultStatus == globalevents.ResultCanceled && resultStatus != globalevents.ResultFailed) {
				resultStatus = logResult.ResultStatus
			}
			if err != nil {
				return globalevents.LogResult{ResultStatus: resultStatus, Content: content}, err
			}
			outputState.Status = DeliveryFallbackDelivered
			active, err = s.persistDeliveryState(ctx, &job, state)
			if err != nil || !active {
				{
					resultStatus := globalevents.ResultFailed
					if err == nil {
						resultStatus = globalevents.ResultSucceeded
					} else if err == context.Canceled {
						resultStatus = globalevents.ResultCanceled
					}
					{
						err := err
						logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
						if err != nil {
							logResult.Content += fmt.Sprintf("\nerror=%v", err)
						}
						return logResult, err
					}
				}
			}
		}
	}

	if meta.Schedule.Mode == ScheduleOnce && s.deliveryComplete(meta, state) {
		logResult, err := s.disableCompletedDelivery(ctx, job.Name, job.DeliveryToken)
		logResult.Content = content + "\n" + logResult.Content
		return logResult, err
	}
	return globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: content}, nil
}

func (s *Service) sendDeliveryOutput(ctx context.Context, jobName string, resolved resolvedCronTarget, out delivery.Output, state CronDeliveryState) (globalevents.LogResult, error) {
	return s.sendOutputsToPlatformTarget(ctx, jobName, resolved.platform, resolved.target, []delivery.Output{out}, state.ReportSessionID, state.ReportMessageID)
}

func (s *Service) persistDeliveryState(ctx context.Context, job *storage.CronJob, state CronDeliveryState) (bool, error) {
	encoded, err := encodeDeliveryState(state)
	if err != nil {
		return false, err
	}
	swapped, err := s.store.CronJobs().CompareAndSwapDelivery(ctx, job.ID, job.DeliveryToken, job.DeliveryToken, encoded)
	if err != nil || !swapped {
		return swapped, err
	}
	job.DeliveryState = encoded
	return true, nil
}

func (s *Service) disableCompletedDelivery(ctx context.Context, jobName, deliveryToken string) (globalevents.LogResult, error) {
	content := "operation=disableCompletedDelivery"
	if s.manager != nil {
		_, err := s.manager.DisableJobIfDeliveryToken(ctx, jobName, deliveryToken)
		{
			resultStatus := globalevents.ResultFailed
			if err == nil {
				resultStatus = globalevents.ResultSucceeded
			} else if err == context.Canceled {
				resultStatus = globalevents.ResultCanceled
			}
			{
				err := err
				logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
				if err != nil {
					logResult.Content += fmt.Sprintf("\nerror=%v", err)
				}
				return logResult, err
			}
		}
	}
	_, err := s.store.CronJobs().DisableByNameIfDeliveryToken(ctx, jobName, deliveryToken)
	{
		resultStatus := globalevents.ResultFailed
		if err == nil {
			resultStatus = globalevents.ResultSucceeded
		} else if err == context.Canceled {
			resultStatus = globalevents.ResultCanceled
		}
		{
			err := err
			logResult := globalevents.LogResult{ResultStatus: resultStatus, Content: content}
			if err != nil {
				logResult.Content += fmt.Sprintf("\nerror=%v", err)
			}
			return logResult, err
		}
	}
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
		platformName := strings.TrimSpace(platform.Name)
		if platformName == "" {
			continue
		}
		ids := uniqueStrings(platform.SuperadminIDs)
		if len(ids) == 0 {
			resolved = append(resolved, resolvedCronTarget{key: platformName + "|superadmins", platform: platformName, target: delivery.Target{Platform: platformName, Superadmins: true}})
			continue
		}
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
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

func (s *Service) cronSandbox(jobName string) sandboxctx.SandboxContext {
	return sandboxctx.SandboxContext{Dir: filepath.Join(s.sandboxRoot, filepath.FromSlash(cronSandboxSubdir(jobName))), Background: true, BackgroundKind: sandboxctx.BackgroundKindCron}
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

func (s *Service) sendOutputsToPlatforms(ctx context.Context, jobName string, platforms []string, outputs []delivery.Output) (globalevents.LogResult, error) {
	return s.sendOutputsToPlatformsMapped(ctx, jobName, platforms, outputs, "", "")
}

func (s *Service) sendOutputsToPlatformTarget(ctx context.Context, jobName, platformName string, target delivery.Target, outputs []delivery.Output, sessionID, messageID string) (globalevents.LogResult, error) {
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
				return logResult, err
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
		receipt, logResult, err := s.sendTarget(ctx, target, []delivery.Output{out})
		content += fmt.Sprintf("\nkind=%s result=%s", out.Kind, logResult.ResultStatus.String())
		if logResult.Content != "" {
			content += "\n" + logResult.Content
		}
		if logResult.ResultStatus == globalevents.ResultFailed || (logResult.ResultStatus == globalevents.ResultCanceled && resultStatus != globalevents.ResultFailed) {
			resultStatus = logResult.ResultStatus
		}
		s.mapReportReceipt(ctx, jobName, sessionID, messageID, receipt)
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
					return logResult, err
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
		return logResult, err
	}
}

func (s *Service) sendOutputsToPlatformsMapped(ctx context.Context, jobName string, platforms []string, outputs []delivery.Output, sessionID, messageID string) (globalevents.LogResult, error) {
	content := "operation=sendOutputsToPlatformsMapped"
	if s.sendTarget == nil {
		{
			err := fmt.Errorf("cron target sender is not configured")
			logResult := globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: content + fmt.Sprintf("\nerror=%q", err.Error())}
			return logResult, err
		}
	}
	resultStatus := globalevents.ResultSucceeded
	var errs []error
	for _, platformName := range uniqueStrings(platforms) {
		if platformName == "" {
			continue
		}
		logResult, err := s.sendOutputsToPlatformTarget(ctx, jobName, platformName, delivery.Target{Platform: platformName, Superadmins: true}, outputs, sessionID, messageID)
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

func (s *Service) mapReportReceipt(ctx context.Context, jobName, sessionID, messageID string, receipt delivery.Receipt) {
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
			if err == context.Canceled {
				return
			}
			{
				content, _ := json.Marshal(map[string]any{"job": jobName, "platform": platformName, "scope_id": scopeID, "platform_message_id": platformMessageID, "session_id": sessionID, "message_id": messageID, "error": err.Error()})
				record := globalevents.LogRecord{
					Category:     globalevents.LogAudit,
					Level:        slog.LevelError,
					EventName:    "cron.report_map_failed",
					Module:       "cron",
					Type:         globalevents.ContentJSON,
					Content:      string(content),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				record.Identity = identity
				if sessionID != "" {
					record.Identity.SessionID = &sessionID
				}
				background.LogBackground(ctx, record)
			}
			{
				record := globalevents.LogRecord{
					Category:     globalevents.LogRuntime,
					Level:        slog.LevelError,
					EventName:    "map_cron_report_message_failed",
					Module:       "cron",
					Type:         globalevents.ContentText,
					Content:      fmt.Sprintf("map cron report message failed\njob=%v\nplatform=%v\nscope_id=%v\nplatform_message_id=%v\nsession_id=%v\nmessage_id=%v\nerror=%v", jobName, platformName, scopeID, platformMessageID, sessionID, messageID, err.Error()),
					ContentLimit: 4096,
					ResultStatus: globalevents.ResultFailed,
				}
				record.Identity = identity
				if sessionID != "" {
					record.Identity.SessionID = &sessionID
				}
				background.LogBackground(ctx, record)
			}
		}
	}
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
	if sourceSessionID == "" || s.store == nil || s.store.Sessions() == nil || s.store.Messages() == nil {
		return nil
	}
	source, err := s.store.Sessions().Get(ctx, sourceSessionID)
	if err != nil {
		return err
	}
	if s.sessions == nil {
		return fmt.Errorf("cron session service is not configured")
	}
	for _, target := range s.enabledPlatforms {
		if target.Name == "" || target.Name == source.Platform {
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
