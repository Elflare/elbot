package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"elbot/internal/agent"
	background "elbot/internal/background/share"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/media"
	"elbot/internal/session"
	"elbot/internal/workspace"
)

type resolvedCronTarget struct {
	key      string
	platform string
	target   delivery.Target
}

type deliveryGate struct {
	token chan struct{} // 单个任务的互斥令牌。
	refs  int           // 持有者与等待者的总数，由 Service.mu 保护。
}

func (s *Service) deliverPrepared(ctx context.Context, job Job, meta Metadata, state CronDeliveryState, platformFilter string, recovery bool) (logResult globalevents.LogResult, err error) {
	logResult = globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "operation=deliverPrepared"}
	defer func() {
		if err != nil {
			logResult.Content += fmt.Sprintf("\nerror=%v", err)
		}
	}()
	if !state.ReportReady {
		return logResult, nil
	}
	taken, err := s.sessionTakenOver(ctx, state.ReportSessionID)
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
	for _, resolved := range s.resolveDeliveryTargets(meta) {
		if platformFilter != "" && resolved.platform != platformFilter {
			continue
		}
		targetState := ensureDeliveryTargetState(&state, resolved.key)
		for index, outputID := range deliveryOutputIDs(state) {
			taken, err := s.sessionTakenOver(ctx, state.ReportSessionID)
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
			if state.NeedReport && strings.TrimSpace(state.Report) != "" {
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
				if segment.Source.Path != "" {
					scope, buildErr = s.cronSandbox(job.Name)
				}
				if buildErr == nil {
					out, buildErr = background.BuildReportSegmentOutput(ctx, segment, scope, s.media, media.ImportLimits{})
				}
				if buildErr != nil && (!recovery || !state.NeedReport || ctx.Err() != nil) {
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
			if buildErr != nil && outputID != "text" && status != DeliveryFallbackDelivered && state.NeedReport && recovery && ctx.Err() == nil {
				logResult.Content += fmt.Sprintf("\nattachment_error=%v", buildErr)
				outputState.Status = DeliveryFallbackPending
				outputState.FallbackText = reportSegmentFallbackText(state.ReportSegments[segmentIndex])
				active, saveErr := s.persistDeliveryState(ctx, &job, state, background.MapReportReceipt(state.ReportSessionID, state.ReportMessageID, receipt))
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
				active, saveErr := s.persistDeliveryState(ctx, &job, state, background.MapReportReceipt(state.ReportSessionID, state.ReportMessageID, receipt))
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

func (s *Service) persistDeliveryState(ctx context.Context, job *Job, state CronDeliveryState, mappings []background.ReportMessageMap) (bool, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	ctx = cleanupCtx
	meta, err := decodeMetadata(job.Metadata)
	if err != nil {
		return false, err
	}
	state.DeliveryComplete = s.deliveryComplete(meta, state)
	encoded, err := encodeDeliveryState(state)
	if err != nil {
		return false, err
	}
	swapped, err := s.store.CompareAndSwapDelivery(ctx, job.ID, job.DeliveryToken, job.DeliveryToken, encoded, mappings)
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

func (s *Service) resolveDeliveryTargets(meta Metadata) []resolvedCronTarget {
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
	for _, target := range s.resolveDeliveryTargets(meta) {
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
	if state.NeedReport && strings.TrimSpace(state.Report) != "" {
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

func reportSegmentFallbackText(segment delivery.Output) string {
	if segment.Source.URL != "" {
		return fmt.Sprintf("url %s 发送失败", segment.Source.URL)
	}
	if segment.Source.MediaID != "" {
		return fmt.Sprintf("媒体 %s 发送失败", segment.Source.MediaID)
	}
	return fmt.Sprintf("路径 %s 附件发送失败", segment.Source.Path)
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
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return CronDeliveryState{}, fmt.Errorf("decode cron delivery state: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return CronDeliveryState{}, fmt.Errorf("decode cron delivery state: unexpected trailing JSON")
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
		gate = &deliveryGate{token: make(chan struct{}, 1)}
		s.deliveryGates[jobName] = gate
	}
	gate.refs++
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		gate.refs--
		if gate.refs == 0 {
			delete(s.deliveryGates, jobName)
		}
	}
	select {
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	case gate.token <- struct{}{}:
		unlock := func() {
			<-gate.token
			release()
		}
		if err := ctx.Err(); err != nil {
			unlock()
			return nil, err
		}
		return unlock, nil
	}
}

func (s *Service) sendToPlatforms(ctx context.Context, jobName string, platforms []string, text string) (globalevents.LogResult, error) {
	return s.sendOutputsToPlatforms(ctx, jobName, platforms, []delivery.Output{delivery.Text(text)})
}

func (s *Service) sendOutputsToPlatformTarget(ctx context.Context, jobName, platformName string, target delivery.Target, outputs []delivery.Output, sessionID string) (delivery.Receipt, globalevents.LogResult, error) {
	identity := cronLogIdentity(ctx)

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
			return receipt, globalevents.LogResult{ResultStatus: resultStatus, Content: content + fmt.Sprintf("\ncontext_error=%q", ctx.Err().Error())}, errors.Join(append(errs, err)...)
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
				Identity:     identity,
			}
			if logResult.ResultStatus == globalevents.ResultFailed || logResult.ResultStatus == globalevents.ResultCanceled {
				record.EventName = "cron.send_failed"
			}
			if logResult.ResultStatus == globalevents.ResultFailed {
				record.Level = slog.LevelError
			}
			if sessionID != "" {
				record.Identity.SessionID = &sessionID
			}
			background.LogBackground(ctx, record)
		}
		if (logResult.ResultStatus == globalevents.ResultFailed || logResult.ResultStatus == globalevents.ResultCanceled) && err != nil && ctx.Err() != nil {
			return receipt, globalevents.LogResult{ResultStatus: resultStatus, Content: content}, errors.Join(errs...)
		}
	}
	return receipt, globalevents.LogResult{ResultStatus: resultStatus, Content: content}, errors.Join(errs...)
}

func (s *Service) sendOutputsToPlatforms(ctx context.Context, jobName string, platforms []string, outputs []delivery.Output) (globalevents.LogResult, error) {
	content := "operation=sendOutputsToPlatforms"

	resultStatus := globalevents.ResultSucceeded
	var errs []error
	for _, platformName := range uniqueStrings(platforms) {
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
			return globalevents.LogResult{ResultStatus: resultStatus, Content: content}, errors.Join(errs...)
		}
	}
	return globalevents.LogResult{ResultStatus: resultStatus, Content: content}, errors.Join(errs...)
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
	for _, target := range s.enabledPlatforms {
		if target.Name == meta.Target.SourcePlatform {
			continue
		}
		owner := targetOwnerID(target)
		if owner == "" {
			continue
		}
		metadata := map[string]json.RawMessage{}
		for key, value := range map[string]any{"cron_job_name": jobName, "cron_source_session_id": sourceSessionID, "cron_broadcast_copy": true} {
			encoded, err := json.Marshal(value)
			if err != nil {
				return err
			}
			metadata[key] = encoded
		}
		copySession, err := s.copyBackground(ctx, agent.BackgroundCopyRequest{Scope: session.Scope{ActorID: owner, Platform: target.Name, PlatformScopeID: cronScopeID(jobName)}, SourceSessionID: sourceSessionID, Kind: "cron", Name: jobName, Title: meta.Title, Metadata: metadata})
		if err != nil {
			return err
		}
		{
			content, _ := json.Marshal(map[string]any{"job": jobName, "source_session_id": sourceSessionID, "target_session_id": copySession, "platform": target.Name})
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
			record.Identity = cronLogIdentity(ctx)
			background.LogBackground(ctx, record)
		}
	}
	return nil
}
