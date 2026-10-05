package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"elbot/internal/background"
	"elbot/internal/delivery"
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

func (s *Service) deliverPrepared(ctx context.Context, job storage.CronJob, meta Metadata, state CronDeliveryState, platformFilter string, recovery bool) error {
	if !state.ReportReady {
		return nil
	}
	if taken, err := background.SessionTakenOver(ctx, s.store, state.ReportSessionID); err != nil {
		return err
	} else if taken {
		state.TakenOver = true
	}
	if state.TakenOver {
		state.Report = ""
		state.ReportSegments = nil
		_, err := s.persistDeliveryState(ctx, &job, state)
		if err != nil {
			return err
		}
		if meta.Schedule.Mode == ScheduleOnce {
			return s.disableCompletedDelivery(ctx, job.Name, job.DeliveryToken)
		}
		return nil
	}
	targets := s.resolveDeliveryTargets(meta, job.Name)
	for _, resolved := range targets {
		if taken, err := background.SessionTakenOver(ctx, s.store, state.ReportSessionID); err != nil {
			return err
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
				if err := s.sendDeliveryOutput(ctx, job.Name, resolved, delivery.Text(text), state); err != nil {
					return err
				}
				outputState.Status = DeliveryDelivered
				active, err := s.persistDeliveryState(ctx, &job, state)
				if err != nil || !active {
					return err
				}
			}
		}
		for index, segment := range state.ReportSegments {
			if taken, err := background.SessionTakenOver(ctx, s.store, state.ReportSessionID); err != nil {
				return err
			} else if taken {
				state.TakenOver = true
				return s.deliverPrepared(ctx, job, meta, state, platformFilter, recovery)
			}
			outputState := ensureDeliveryOutputState(targetState, fmt.Sprintf("segment:%d", index))
			if deliveryStatusDone(outputState.Status) {
				continue
			}
			if outputState.Status == DeliveryFallbackPending {
				if err := s.sendDeliveryOutput(ctx, job.Name, resolved, delivery.Text(outputState.FallbackText), state); err != nil {
					return err
				}
				outputState.Status = DeliveryFallbackDelivered
				active, err := s.persistDeliveryState(ctx, &job, state)
				if err != nil || !active {
					return err
				}
				continue
			}

			out, buildErr := background.BuildReportSegmentOutput(segment, s.cronSandbox(job.Name))
			if buildErr == nil {
				buildErr = s.sendDeliveryOutput(ctx, job.Name, resolved, out, state)
			}
			if buildErr == nil {
				outputState.Status = DeliveryDelivered
				active, err := s.persistDeliveryState(ctx, &job, state)
				if err != nil || !active {
					return err
				}
				continue
			}
			if !recovery || isContextCancellation(ctx, buildErr) {
				return buildErr
			}

			outputState.Status = DeliveryFallbackPending
			outputState.FallbackText = reportSegmentFallbackText(segment)
			active, err := s.persistDeliveryState(ctx, &job, state)
			if err != nil || !active {
				return errors.Join(buildErr, err)
			}
			if err := s.sendDeliveryOutput(ctx, job.Name, resolved, delivery.Text(outputState.FallbackText), state); err != nil {
				return err
			}
			outputState.Status = DeliveryFallbackDelivered
			active, err = s.persistDeliveryState(ctx, &job, state)
			if err != nil || !active {
				return err
			}
		}
	}

	if meta.Schedule.Mode == ScheduleOnce && s.deliveryComplete(meta, state) {
		return s.disableCompletedDelivery(ctx, job.Name, job.DeliveryToken)
	}
	return nil
}

func (s *Service) sendDeliveryOutput(ctx context.Context, jobName string, resolved resolvedCronTarget, out delivery.Output, state CronDeliveryState) error {
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

func (s *Service) disableCompletedDelivery(ctx context.Context, jobName, deliveryToken string) error {
	if s.manager != nil {
		_, err := s.manager.DisableJobIfDeliveryToken(ctx, jobName, deliveryToken)
		return err
	}
	_, err := s.store.CronJobs().DisableByNameIfDeliveryToken(ctx, jobName, deliveryToken)
	return err
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

func (s *Service) sendToTargets(ctx context.Context, jobName string, meta Metadata, text string) error {
	if meta.Target.AllEnabledPlatforms {
		return s.sendToPlatforms(ctx, jobName, s.enabledPlatformNames(), text)
	}
	return s.sendToPlatforms(ctx, jobName, []string{meta.Target.SourcePlatform}, text)
}

func (s *Service) sendToPlatforms(ctx context.Context, jobName string, platforms []string, text string) error {
	return s.sendOutputsToPlatforms(ctx, jobName, platforms, []delivery.Output{delivery.Text(text)})
}

func (s *Service) sendOutputsToPlatforms(ctx context.Context, jobName string, platforms []string, outputs []delivery.Output) error {
	return s.sendOutputsToPlatformsMapped(ctx, jobName, platforms, outputs, "", "")
}

func (s *Service) sendOutputsToPlatformTargets(ctx context.Context, jobName string, platforms []PlatformTarget, outputs []delivery.Output, sessionID, messageID string) error {
	if s.sendTarget == nil {
		return fmt.Errorf("cron target sender is not configured")
	}
	var errs []error
	for _, platform := range platforms {
		platformName := strings.TrimSpace(platform.Name)
		if platformName == "" {
			continue
		}
		ids := uniqueStrings(platform.SuperadminIDs)
		if len(ids) == 0 {
			errs = append(errs, s.sendOutputsToPlatformTarget(ctx, jobName, platformName, delivery.Target{Platform: platformName, Superadmins: true}, outputs, sessionID, messageID))
			continue
		}
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			target := delivery.Target{Platform: platformName, PrivateUserID: id}
			errs = append(errs, s.sendOutputsToPlatformTarget(ctx, jobName, platformName, target, outputs, sessionID, messageID))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) sendOutputsToPlatformTarget(ctx context.Context, jobName, platformName string, target delivery.Target, outputs []delivery.Output, sessionID, messageID string) error {
	var errs []error
	for _, out := range outputs {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		attrs := []any{"job", jobName, "platform", platformName, "target", cronTargetLabel(target), "kind", out.Kind}
		s.auditEvent("cron.send_started", attrs...)
		s.logInfo("cron send started", attrs...)
		receipt, err := s.sendTarget(ctx, target, []delivery.Output{out})
		s.mapReportReceipt(ctx, jobName, sessionID, messageID, receipt)
		if err != nil {
			err = fmt.Errorf("send %s: %w", platformName, err)
			if isContextCancellation(ctx, err) {
				return errors.Join(append(errs, err)...)
			}
			errs = append(errs, err)
			s.auditEvent("cron.send_failed", append(attrs, "error", err.Error())...)
			s.logWarn("cron send failed", append(attrs, "error", err.Error())...)
			continue
		}
		s.auditEvent("cron.send_completed", attrs...)
		s.logInfo("cron send completed", attrs...)
	}
	return errors.Join(errs...)
}

func (s *Service) sendOutputsToPlatformsMapped(ctx context.Context, jobName string, platforms []string, outputs []delivery.Output, sessionID, messageID string) error {
	if s.sendTarget == nil {
		return fmt.Errorf("cron target sender is not configured")
	}
	var errs []error
	for _, platformName := range uniqueStrings(platforms) {
		if platformName == "" {
			continue
		}
		for _, out := range outputs {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
			attrs := []any{"job", jobName, "platform", platformName, "target", "superadmins", "kind", out.Kind}
			s.auditEvent("cron.send_started", attrs...)
			s.logInfo("cron send started", attrs...)
			receipt, err := s.sendTarget(ctx, delivery.Target{Platform: platformName, Superadmins: true}, []delivery.Output{out})
			s.mapReportReceipt(ctx, jobName, sessionID, messageID, receipt)
			if err != nil {
				err = fmt.Errorf("send %s: %w", platformName, err)
				if isContextCancellation(ctx, err) {
					return errors.Join(append(errs, err)...)
				}
				errs = append(errs, err)
				s.auditEvent("cron.send_failed", "job", jobName, "platform", platformName, "target", "superadmins", "kind", out.Kind, "error", err.Error())
				s.logWarn("cron send failed", "job", jobName, "platform", platformName, "target", "superadmins", "kind", out.Kind, "error", err.Error())
				continue
			}
			s.auditEvent("cron.send_completed", attrs...)
			s.logInfo("cron send completed", attrs...)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) mapReportReceipt(ctx context.Context, jobName, sessionID, messageID string, receipt delivery.Receipt) {
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
			if isContextCancellation(ctx, err) {
				return
			}
			s.auditEvent("cron.report_map_failed", "job", jobName, "platform", platformName, "scope_id", scopeID, "platform_message_id", platformMessageID, "session_id", sessionID, "message_id", messageID, "error", err.Error())
			s.logWarn("map cron report message failed", "job", jobName, "platform", platformName, "scope_id", scopeID, "platform_message_id", platformMessageID, "session_id", sessionID, "message_id", messageID, "error", err.Error())
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
		s.auditEvent("cron.session_copied", "job", jobName, "source_session_id", sourceSessionID, "target_session_id", copySession.ID, "platform", target.Name)
	}
	return nil
}
