package cron

import (
	"context"
	"elbot/internal/contextinfo"
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

type deliveryGate struct {
	token chan struct{} // 单个任务的互斥令牌。
	refs  int           // 持有者与等待者的总数，由 Service.mu 保护。
}

func (s *Service) deliverPrepared(ctx context.Context, job Job, meta Metadata, state CronDeliveryState, platformFilter string, recovery bool) (err error) {
	if !state.ReportReady {
		return nil
	}
	taken, err := s.sessionTakenOver(ctx, state.ReportSessionID)
	if err != nil {
		return err
	}
	state.TakenOver = state.TakenOver || taken
	if state.TakenOver {
		state.Report, state.ReportSegments = "", nil
		if _, err := s.persistDeliveryState(ctx, job, state, nil); err != nil {

			return err
		}
		if meta.Schedule.Mode == ScheduleOnce {
			return s.disableCompletedDelivery(ctx, job.Name, job.DeliveryToken)
		}
		return nil
	}
	for _, resolved := range s.resolveDeliveryTargets(meta) {
		if platformFilter != "" && resolved.Platform != platformFilter {
			continue
		}
		targetState := ensureDeliveryTargetState(&state, resolved)
		for index, outputID := range deliveryOutputIDs(state) {
			taken, err := s.sessionTakenOver(ctx, state.ReportSessionID)
			if err != nil {
				return err
			}
			if taken {
				state.TakenOver = true
				return s.deliverPrepared(ctx, job, meta, state, platformFilter, recovery)
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
			}
			var receipt delivery.Receipt
			if buildErr == nil {

				receipt, buildErr = s.sendDeliveryOutput(ctx, job.Name, resolved, out, state.ReportSessionID)
			}
			if buildErr != nil && outputID != "text" && status != DeliveryFallbackDelivered && state.NeedReport && recovery && ctx.Err() == nil {

				outputState.Status = DeliveryFallbackPending
				outputState.FallbackText = reportSegmentFallbackText(state.ReportSegments[segmentIndex])
				active, saveErr := s.persistDeliveryState(ctx, job, state, background.MapReportReceipt(state.ReportSessionID, state.ReportMessageID, receipt))
				if saveErr != nil {

					return errors.Join(buildErr, saveErr)
				}
				if !active {
					return buildErr
				}

				receipt, buildErr = s.sendDeliveryOutput(ctx, job.Name, resolved, delivery.Text(outputState.FallbackText), state.ReportSessionID)
				status = DeliveryFallbackDelivered
			}
			if buildErr == nil {
				outputState.Status = status
			}
			if buildErr == nil || len(receipt.SentMessages) > 0 {
				active, saveErr := s.persistDeliveryState(ctx, job, state, background.MapReportReceipt(state.ReportSessionID, state.ReportMessageID, receipt))
				if saveErr != nil {

					return errors.Join(buildErr, saveErr)
				}
				if !active {
					return buildErr
				}
			}
			if buildErr != nil {
				return buildErr
			}
		}
	}
	if meta.Schedule.Mode == ScheduleOnce && s.deliveryComplete(meta, state) {
		return s.disableCompletedDelivery(ctx, job.Name, job.DeliveryToken)
	}
	return nil
}

func (s *Service) sendDeliveryOutput(ctx context.Context, jobName string, target delivery.Target, out delivery.Output, sessionID string) (delivery.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return delivery.Receipt{}, err
	}
	identity := cronLogIdentity(ctx)
	{
		content, _ := json.Marshal(map[string]any{"job": jobName, "platform": target.Platform, "target": cronTargetLabel(target), "kind": out.Kind})
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
		record.Identity.SessionID = sessionID
		background.LogBackground(ctx, record)
	}
	receipt, err := s.sendTarget(ctx, target, []delivery.Output{out})
	status, detail := globalevents.ResultSucceeded, ""
	if err != nil {
		status, detail = globalevents.ResultFailed, err.Error()
		if globalevents.LogStatus(err) == globalevents.ResultCanceled {
			status = globalevents.ResultCanceled
		}
		err = fmt.Errorf("send %s: %w", target.Platform, err)
	}
	{
		content, _ := json.Marshal(map[string]any{"job": jobName, "platform": target.Platform, "target": cronTargetLabel(target), "kind": out.Kind, "content": detail})
		record := globalevents.LogRecord{
			Category:     globalevents.LogAudit,
			Level:        slog.LevelInfo,
			EventName:    "cron.send_completed",
			Module:       "cron",
			Type:         globalevents.ContentJSON,
			Content:      string(content),
			ContentLimit: 4096,
			ResultStatus: status,
			Identity:     identity,
		}
		if err != nil {
			record.EventName = "cron.send_failed"
		}
		if status == globalevents.ResultFailed {
			record.Level = slog.LevelWarn
		}
		record.Identity.SessionID = sessionID
		background.LogBackground(ctx, record)
	}
	return receipt, err
}

func (s *Service) persistDeliveryState(ctx context.Context, job Job, state CronDeliveryState, mappings []background.ReportMessageMap) (active bool, err error) {

	defer func() {
		if err == nil {
			return
		}
		record := globalevents.LogRecord{Category: globalevents.LogRuntime, Level: slog.LevelError, Module: "cron", EventName: "persist_cron_delivery_failed", ResultStatus: globalevents.ResultFailed, Identity: cronLogIdentity(ctx), Content: fmt.Sprintf("job=%s error=%v", job.Name, err)}
		if globalevents.LogStatus(err) == globalevents.ResultCanceled {
			record.Level, record.ResultStatus = slog.LevelInfo, globalevents.ResultCanceled
		}
		background.LogBackground(ctx, record)
	}()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	ctx = cleanupCtx
	meta, err := decodeMetadata(job.Metadata)
	if err != nil {
		return false, err
	}
	state.DeliveryComplete = s.deliveryComplete(meta, state)
	encoded := encodeDeliveryState(state)
	return s.store.CompareAndSwapDelivery(ctx, job.ID, job.DeliveryToken, job.DeliveryToken, encoded, mappings)
}

func (s *Service) disableCompletedDelivery(ctx context.Context, jobName, deliveryToken string) error {
	_, err := s.manager.DisableJobIfDeliveryToken(ctx, jobName, deliveryToken)
	return err
}

func (s *Service) resolveDeliveryTargets(meta Metadata) []delivery.Target {
	platforms := s.enabledPlatforms
	if !meta.Target.AllEnabledPlatforms {
		if !containsString(s.enabledPlatformNames(), meta.Target.SourcePlatform) {
			return nil
		}
		platforms = []PlatformTarget{{Name: meta.Target.SourcePlatform}}
		if id := strings.TrimSpace(meta.CreatedBy.ID); id != "" {
			platforms[0].SuperadminIDs = []string{id}
		}
	}
	resolved := []delivery.Target{}
	for _, platform := range platforms {
		platformName := platform.Name
		ids := uniqueStrings(platform.SuperadminIDs)
		if len(ids) == 0 {
			resolved = append(resolved, delivery.Target{Platform: platformName, Superadmins: true})
			continue
		}
		for _, id := range ids {
			resolved = append(resolved, delivery.Target{Platform: platformName, ChatType: "private", ChatID: id})
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
		targetState := findDeliveryTargetState(state, target)
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

func ensureDeliveryTargetState(state *CronDeliveryState, target delivery.Target) *CronDeliveryTargetState {
	for index := range state.Targets {
		if state.Targets[index].Target == target {
			return &state.Targets[index]
		}
	}
	state.Targets = append(state.Targets, CronDeliveryTargetState{Target: target})
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

func findDeliveryTargetState(state CronDeliveryState, target delivery.Target) *CronDeliveryTargetState {
	for index := range state.Targets {
		if state.Targets[index].Target == target {
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

func encodeDeliveryState(state CronDeliveryState) string {
	data, _ := json.Marshal(state)
	return string(data)
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

func cronTargetLabel(target delivery.Target) string {
	if target.Superadmins {
		return "superadmins"
	}
	return string(target.ChatType)
}

func (s *Service) copySessionToBroadcastTargets(ctx context.Context, sourceSessionID string, meta Metadata, jobName string) error {
	for _, target := range s.enabledPlatforms {
		if target.Name == meta.Target.SourcePlatform {
			continue
		}
		if len(target.SuperadminIDs) == 0 {
			continue
		}
		metadata := map[string]json.RawMessage{}
		for key, value := range map[string]any{"cron_job_name": jobName, "cron_source_session_id": sourceSessionID, "cron_broadcast_copy": true} {
			encoded, _ := json.Marshal(value)
			metadata[key] = encoded
		}
		copySession, err := s.copyBackground(ctx, agent.BackgroundCopyRequest{Scope: session.Scope{Owner: contextinfo.ActorIdentity{Platform: target.Name, ID: target.SuperadminIDs[0]}, Platform: target.Name, TaskType: "cron", TaskID: jobName}, SourceSessionID: sourceSessionID, TaskType: "cron", TaskID: jobName, Name: meta.Title, Metadata: metadata})
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
