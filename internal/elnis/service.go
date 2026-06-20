package elnis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	"encoding/base64"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"elbot/internal/llm"
	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/elyph"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
)

type SenderFunc func(ctx context.Context, target delivery.Target, out delivery.Output) error

type AuditFunc func(event string, attrs ...any)

type QueuedLLMEvent struct {
	Event   Event
	EventID string
}

type EnqueueLLMFunc func(ctx context.Context, event QueuedLLMEvent) error

type ModelResolverFunc func(slot string) config.ModelSelection

type Options struct {
	Config       config.ElnisConfig
	SandboxRoot  string
	Tokens       map[string]string
	Store        storage.Store
	Logger       *slog.Logger
	Audit        AuditFunc
	Send         SenderFunc
	Runner       background.Runner
	ResolveModel ModelResolverFunc
}

type Service struct {
	cfg          config.ElnisConfig
	sandboxRoot  string
	tokens       map[string]string
	store        storage.Store
	logger       *slog.Logger
	audit        AuditFunc
	send         SenderFunc
	runner       background.Runner
	resolveModel ModelResolverFunc
	enqueueLLM   EnqueueLLMFunc
}

func NewService(opts Options) (*Service, error) {
	if opts.SandboxRoot == "" {
		opts.SandboxRoot = filepath.Join("data", "sandbox")
	}
	if opts.Store == nil || opts.Store.ElnisEvents() == nil {
		return nil, fmt.Errorf("elnis event store is not configured")
	}
	if opts.Config.Enabled && len(opts.Tokens) == 0 {
		return nil, fmt.Errorf("elnis enabled but no tokens are configured")
	}
	return &Service{cfg: opts.Config, sandboxRoot: opts.SandboxRoot, tokens: opts.Tokens, store: opts.Store, logger: opts.Logger, audit: opts.Audit, send: opts.Send, runner: opts.Runner, resolveModel: opts.ResolveModel}, nil
}

func (s *Service) SetLLMEnqueuer(enqueue EnqueueLLMFunc) {
	s.enqueueLLM = enqueue
}

func (s *Service) Handle(ctx context.Context, token string, req Request) (Response, error) {
	tokenName, ok := s.authenticate(token)
	if !ok {
		s.auditEvent("elnis.auth_failed")
		return Response{Accepted: false, Status: StatusFailed, Error: "unauthorized"}, fmt.Errorf("unauthorized")
	}
	event, err := s.prepareEvent(tokenName, req)
	if err != nil {
		s.auditEvent("elnis.rejected", "token_name", tokenName, "error", err.Error())
		return Response{Accepted: false, Status: StatusFailed, Error: err.Error()}, err
	}
	attrs := s.eventAttrs(event)
	if err := s.authorizeElwisp(event); err != nil {
		s.auditEvent("elnis.permission_denied", append(attrs, "error", err.Error())...)
		s.logWarn("elnis permission denied", append(attrs, "error", err.Error())...)
		return Response{Accepted: false, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
	}
	if err := s.authorizeInternalTools(event); err != nil {
		s.auditEvent("elnis.tool_denied", append(attrs, "error", err.Error())...)
		s.logWarn("elnis internal tool denied", append(attrs, "error", err.Error())...)
		return Response{Accepted: false, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
	}
	if err := s.authorizeExternalTools(event); err != nil {
		s.auditEvent("elnis.external_tool_denied", append(attrs, "error", err.Error())...)
		s.logWarn("elnis external tool denied", append(attrs, "error", err.Error())...)
		return Response{Accepted: false, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
	}
	if existing, err := s.store.ElnisEvents().GetByKey(ctx, req.Elwisp.Name, req.Source, req.ID); err == nil {
		s.handleDuplicate(event, existing)
		return Response{Accepted: true, Duplicate: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusDuplicate}, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return Response{}, err
	}

	status := StatusAccepted
	result := ""
	eventErr := ""
	if req.Mode == ModeLLM {
		status = StatusQueued
	}
	record, err := s.store.ElnisEvents().Create(ctx, storage.CreateElnisEventRequest{
		EventKey:         event.EventKey,
		TokenName:        event.TokenName,
		ElwispName:       req.Elwisp.Name,
		Source:           req.Source,
		SourceID:         req.ID,
		Tags:             event.TagsJSON,
		Mode:             req.Mode,
		ModelSlot:        req.ModelSlot,
		ContentHash:      event.ContentHash,
		ToolDeclarations: event.ToolDeclarations,
		ToolHash:         event.ToolHash,
		RequestedTargets: event.RequestedTargets,
		ResolvedTargets:  event.ResolvedTargets,
		Status:           status,
		Result:           result,
		Error:            eventErr,
		ReceivedAt:       event.ReceivedAt,
		CreatedAt:        event.CreatedAt,
	})
	if err != nil {
		return Response{}, err
	}
	attrs = append(attrs, "event_id", record.ID)
	s.auditEvent("elnis.accepted", attrs...)
	s.logInfo("elnis event accepted", attrs...)

	switch req.Mode {
	case ModeRecord:
		if err := s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusCompleted, "", ""); err != nil {
			return Response{}, err
		}
		s.auditEvent("elnis.recorded", attrs...)
		return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusCompleted}, nil
	case ModeDirect:
		if err := s.runDirect(ctx, event, record.ID); err != nil {
			s.auditEvent("elnis.direct_failed", append(attrs, "error", err.Error())...)
			s.logWarn("elnis direct failed", append(attrs, "error", err.Error())...)
			_ = s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusFailed, "", err.Error())
			return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
		}
		s.auditEvent("elnis.direct_completed", attrs...)
		return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusCompleted}, nil
	case ModeLLM:
		s.auditEvent("elnis.llm_queued", attrs...)
		s.logInfo("elnis llm queued", attrs...)
		if s.enqueueLLM != nil {
			if err := s.enqueueLLM(ctx, QueuedLLMEvent{Event: event, EventID: record.ID}); err != nil {
				_ = s.completeEvent(ctx, record.ID, event.ResolvedTargets, StatusFailed, "", err.Error())
				s.logWarn("elnis llm enqueue failed", append(attrs, "error", err.Error())...)
				return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusFailed, Error: err.Error()}, err
			}
		}
		return Response{Accepted: true, EventKey: event.EventKey, Mode: req.Mode, Status: StatusQueued}, nil
	default:
		return Response{}, fmt.Errorf("unsupported mode %q", req.Mode)
	}
}

func (s *Service) authenticate(token string) (string, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	for name, value := range s.tokens {
		if value != "" && token == value {
			return name, true
		}
	}
	return "", false
}

func (s *Service) prepareEvent(tokenName string, req Request) (Event, error) {
	req.Version = strings.TrimSpace(req.Version)
	req.Elwisp.Name = strings.TrimSpace(req.Elwisp.Name)
	req.Source = strings.TrimSpace(req.Source)
	req.ID = strings.TrimSpace(req.ID)
	req.Mode = strings.TrimSpace(req.Mode)
	req.Format = strings.TrimSpace(req.Format)
	req.ModelSlot = strings.TrimSpace(req.ModelSlot)
	req.Content = strings.TrimSpace(req.Content)
	if req.Version != "elvena.v1" {
		return Event{}, fmt.Errorf("unsupported ELvena version %q", req.Version)
	}
	if req.Elwisp.Name == "" {
		return Event{}, fmt.Errorf("elwisp.name is required")
	}
	if req.Source == "" {
		return Event{}, fmt.Errorf("source is required")
	}
	if req.ID == "" {
		return Event{}, fmt.Errorf("id is required")
	}
	if req.Content == "" {
		return Event{}, fmt.Errorf("content is required")
	}
	if req.Format == "" {
		req.Format = "text"
	}
	if req.Format != "text" && req.Format != "elyph" {
		return Event{}, fmt.Errorf("unsupported format %q", req.Format)
	}
	if req.Mode != ModeRecord && req.Mode != ModeDirect && req.Mode != ModeLLM {
		return Event{}, fmt.Errorf("unsupported mode %q", req.Mode)
	}
	if req.ModelSlot != "" && !isElnisModelSlot(req.ModelSlot) {
		return Event{}, fmt.Errorf("unsupported model_slot %q", req.ModelSlot)
	}
	createdAt := time.Now()
	if strings.TrimSpace(req.CreatedAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(req.CreatedAt))
		if err != nil {
			return Event{}, fmt.Errorf("created_at must be RFC3339: %w", err)
		}
		createdAt = parsed
	}
	tagsJSON, err := json.Marshal(trimStrings(req.Elwisp.Tags))
	if err != nil {
		return Event{}, err
	}
	requestedTargets, err := json.Marshal(normalizeTargets(req.Targets))
	if err != nil {
		return Event{}, err
	}
	resolved, err := s.resolveTargets(req)
	if err != nil {
		return Event{}, err
	}
	resolvedTargets, err := json.Marshal(resolved)
	if err != nil {
		return Event{}, err
	}
	toolDeclarations, err := normalizedToolDeclarations(req.Tools)
	if err != nil {
		return Event{}, err
	}
	return Event{
		Request:          req,
		TokenName:        tokenName,
		EventKey:         req.Elwisp.Name + "/" + req.Source + "/" + req.ID,
		ContentHash:      contentHash(req),
		ToolDeclarations: toolDeclarations,
		ToolHash:         hashText(toolDeclarations),
		TagsJSON:         string(tagsJSON),
		RequestedTargets: string(requestedTargets),
		ResolvedTargets:  string(resolvedTargets),
		CreatedAt:        createdAt,
		ReceivedAt:       time.Now(),
	}, nil
}

func (s *Service) authorizeElwisp(event Event) error {
	policy, ok := s.cfg.Elwisps[event.Request.Elwisp.Name]
	if !ok {
		return nil
	}
	if policy.Enabled != nil && !*policy.Enabled {
		return fmt.Errorf("elwisp %q is disabled", event.Request.Elwisp.Name)
	}
	allowedTokens := trimStrings(policy.AllowedTokens)
	if len(allowedTokens) == 0 {
		return nil
	}
	for _, token := range allowedTokens {
		if token == event.TokenName {
			return nil
		}
	}
	return fmt.Errorf("token %q is not allowed for elwisp %q", event.TokenName, event.Request.Elwisp.Name)
}

func (s *Service) authorizeInternalTools(event Event) error {
	allowed := s.allowedInternalTools(event.Request.Elwisp.Name)
	for _, name := range backgroundToolNames(event.Request.ToolListNames) {
		if name == "discover_tool" {
			continue
		}
		if !allowed[name] {
			return fmt.Errorf("tool %q is not allowed for elwisp %q", name, event.Request.Elwisp.Name)
		}
	}
	return nil
}

func (s *Service) allowedInternalTools(elwispName string) map[string]bool {
	allowedTools := s.cfg.AllowedTools
	if policy, ok := s.cfg.Elwisps[elwispName]; ok && policy.AllowedTools != nil {
		allowedTools = policy.AllowedTools
	}
	return setFromStrings(allowedTools)
}

func (s *Service) authorizeExternalTools(event Event) error {
	disabled := map[string]bool{}
	if policy, ok := s.cfg.Elwisps[event.Request.Elwisp.Name]; ok {
		disabled = setFromStrings(policy.DisabledExternalTools)
	}
	seen := map[string]bool{}
	for _, declared := range event.Request.Tools {
		name := strings.TrimSpace(declared.Name)
		if name == "" {
			return fmt.Errorf("external tool name is required")
		}
		if seen[name] {
			return fmt.Errorf("external tool %q is duplicated", name)
		}
		seen[name] = true
		if disabled[name] {
			return fmt.Errorf("external tool %q is disabled for elwisp %q", name, event.Request.Elwisp.Name)
		}
		if strings.ContainsAny(name, ". /\\") {
			return fmt.Errorf("external tool %q has invalid name", name)
		}
		if strings.TrimSpace(declared.Endpoint) == "" {
			return fmt.Errorf("external tool %q endpoint is required", name)
		}
		parsed, err := url.Parse(strings.TrimSpace(declared.Endpoint))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("external tool %q endpoint must be http or https URL", name)
		}
		if declared.TimeoutSeconds < 0 || declared.TimeoutSeconds > 60 {
			return fmt.Errorf("external tool %q timeout_seconds must be between 0 and 60", name)
		}
		if len(declared.Schema) == 0 {
			return fmt.Errorf("external tool %q schema is required", name)
		}
		if schemaType, _ := declared.Schema["type"].(string); schemaType != "object" {
			return fmt.Errorf("external tool %q schema.type must be object", name)
		}
	}
	return nil
}

func (s *Service) elwispCachedTools(event Event) []toolrun.CachedTool {
	return toolrun.CachedToolsFromELwisp(toolrun.ELwispInjection{ELwispName: event.Request.Elwisp.Name, EventKey: event.EventKey, Tools: event.Request.Tools})
}

func (s *Service) resolveTargets(req Request) (Targets, error) {
	policy := s.cfg.Delivery
	if elwispPolicy, ok := s.cfg.Elwisps[req.Elwisp.Name]; ok {
		if len(elwispPolicy.Delivery.DefaultPlatforms) > 0 {
			policy.DefaultPlatforms = elwispPolicy.Delivery.DefaultPlatforms
		}
		if elwispPolicy.Delivery.AllowSuperadmins {
			policy.AllowSuperadmins = true
		}
	}
	allowed := setFromStrings(policy.DefaultPlatforms)
	requested := trimStrings(req.Targets.Platforms)
	if len(requested) == 0 || hasAllTarget(requested) {
		requested = trimStrings(policy.DefaultPlatforms)
	}
	platforms := []string{}
	for _, platform := range requested {
		if platform == "all" {
			continue
		}
		if allowed[platform] {
			platforms = append(platforms, platform)
		}
	}
	platforms = uniqueSorted(platforms)
	if len(platforms) == 0 && req.Mode == ModeDirect {
		return Targets{}, fmt.Errorf("no allowed delivery platforms")
	}
	return Targets{Platforms: platforms, Superadmins: policy.AllowSuperadmins && (req.Targets.Superadmins || req.Mode == ModeDirect)}, nil
}

func (s *Service) handleDuplicate(event Event, existing *storage.ElnisEvent) {
	attrs := s.eventAttrs(event)
	if existing != nil && existing.ContentHash != event.ContentHash {
		s.logWarn("elnis duplicate event hash mismatch", append(attrs, "existing_event_id", existing.ID)...)
	} else {
		s.logInfo("elnis duplicate event", attrs...)
	}
	s.auditEvent("elnis.duplicate", attrs...)
}

func (s *Service) runDirect(ctx context.Context, event Event, eventID string) error {
	if s.send == nil {
		return fmt.Errorf("elnis sender is not configured")
	}
	req := event.Request
	// Download segments to sandbox
	paths, err := s.downloadSegments(ctx, req.Elwisp.Name, req.ID, req.Segments)
	if err != nil {
		return fmt.Errorf("download segments: %w", err)
	}
	resolved := Targets{}
	if err := json.Unmarshal([]byte(event.ResolvedTargets), &resolved); err != nil {
		return err
	}
	if !resolved.Superadmins {
		return fmt.Errorf("direct delivery only supports superadmins in phase 1")
	}
	// Build outputs: segments first, content as fallback
	outputs := buildDirectOutputs(req, paths)
	for _, platformName := range resolved.Platforms {
		for _, out := range outputs {
			if err := s.send(ctx, delivery.Target{Platform: platformName, Superadmins: true}, out); err != nil {
				return err
			}
		}
	}
	return s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusCompleted, "", "")
}

func (s *Service) RunLLMEvent(ctx context.Context, event Event, eventID string) error {
	attrs := s.eventAttrs(event)
	if s.runner == nil {
		err := fmt.Errorf("elnis background runner is not configured")
		_ = s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusFailed, "", err.Error())
		s.logWarn("elnis llm failed", append(attrs, "event_id", eventID, "error", err.Error())...)
		return err
	}
	if err := s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusRunning, "", ""); err != nil {
		return err
	}
	s.auditEvent("elnis.llm_started", append(attrs, "event_id", eventID)...)
	s.logInfo("elnis llm started", append(attrs, "event_id", eventID)...)
	model := s.modelForEvent(event)
	result, err := s.runner.RunBackground(ctx, background.RunRequest{
		Kind:          background.KindElnis,
		Name:          event.EventKey,
		Title:         firstNonEmpty(event.Request.Title, "Elnis: "+event.Request.Source),
		Platform:      firstPlatform(event.ResolvedTargets),
		Actor:         elnisActor(event),
		ScopeID:       "elnis:" + event.EventKey,
		ModelProvider: model.Provider,
		Model:         model.Model,
		PromptSegments: segmentsLLM(event.Request.Segments),
		Prompt:        s.llmPrompt(event),
		ToolListNames: event.Request.ToolListNames,
		CachedTools:   s.elwispCachedTools(event),
		SandboxSubdir: "elnis/" + event.Request.Elwisp.Name,
		Metadata: map[string]string{
			"elnis_event_key": event.EventKey,
			"elwisp_name":     event.Request.Elwisp.Name,
			"elnis_source":    event.Request.Source,
			"elnis_source_id": event.Request.ID,
		},
	})
	if err != nil {
		_ = s.completeEvent(ctx, eventID, event.ResolvedTargets, StatusFailed, "", err.Error())
		s.auditEvent("elnis.llm_failed", append(attrs, "event_id", eventID, "error", err.Error())...)
		s.logWarn("elnis llm failed", append(attrs, "event_id", eventID, "error", err.Error())...)
		return err
	}
	parsed, parseErr := background.ParseJSONResult(result.Text)
	if parseErr != nil {
		result, parsed, parseErr = s.retryLLMResultFormat(ctx, event, result.SessionID, model)
	}
	if parseErr != nil {
		message := fmt.Sprintf("Elnis 事件 %s 解析格式失败，请查看后台 session。\nsession: %s\n错误：%v", event.EventKey, result.SessionID, parseErr)
		_ = s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusFailed, result.SessionID, message, parseErr.Error())
		s.auditEvent("elnis.llm_failed", append(attrs, "event_id", eventID, "session_id", result.SessionID, "error", parseErr.Error())...)
		s.logWarn("elnis llm format failed", append(attrs, "event_id", eventID, "session_id", result.SessionID, "error", parseErr.Error())...)
		return parseErr
	}
	resultJSON, _ := json.Marshal(parsed)
	if err := s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusCompleted, result.SessionID, string(resultJSON), ""); err != nil {
		return err
	}
	if parsed.NeedReport && strings.TrimSpace(parsed.Report) != "" {
		if err := s.sendReport(ctx, event, parsed.Report, parsed.ReportSegments); err != nil {
			_ = s.completeEventWithSession(ctx, eventID, event.ResolvedTargets, StatusFailed, result.SessionID, string(resultJSON), err.Error())
			s.logWarn("elnis llm report failed", append(attrs, "event_id", eventID, "session_id", result.SessionID, "error", err.Error())...)
			return err
		}
	}
	s.auditEvent("elnis.llm_completed", append(attrs, "event_id", eventID, "session_id", result.SessionID)...)
	s.logInfo("elnis llm completed", append(attrs, "event_id", eventID, "session_id", result.SessionID)...)
	return nil
}

func (s *Service) retryLLMResultFormat(ctx context.Context, event Event, sessionID string, model config.ModelSelection) (background.RunResult, background.JSONResult, error) {
	result, err := s.runner.RunBackground(ctx, background.RunRequest{
		Kind:          background.KindElnis,
		Name:          event.EventKey,
		Title:         firstNonEmpty(event.Request.Title, "Elnis: "+event.Request.Source),
		Platform:      firstPlatform(event.ResolvedTargets),
		Actor:         elnisActor(event),
		ScopeID:       "elnis:" + event.EventKey,
		SessionID:     sessionID,
		ModelProvider: model.Provider,
		Model:         model.Model,
		Prompt:        background.DefaultJSONRetryPrompt(),
		SandboxSubdir: "elnis/" + event.Request.Elwisp.Name,
		Metadata:      map[string]string{"elnis_event_key": event.EventKey, "elwisp_name": event.Request.Elwisp.Name},
	})
	if err != nil {
		return result, background.JSONResult{}, err
	}
	parsed, err := background.ParseJSONResult(result.Text)
	return result, parsed, err
}

func (s *Service) sendReport(ctx context.Context, event Event, report string, reportSegments []llm.MessageSegment) error {
	if s.send == nil {
		return fmt.Errorf("elnis sender is not configured")
	}
	resolved := Targets{}
	if err := json.Unmarshal([]byte(event.ResolvedTargets), &resolved); err != nil {
		return err
	}
	if !resolved.Superadmins {
		return nil
	}
	// Send report text first
	outputs := []delivery.Output{delivery.Text(report)}
	// Append segment outputs using sandbox paths
	for _, seg := range reportSegments {
		switch seg.Type {
		case llm.SegmentImage:
			if seg.URL != "" {
				outputs = append(outputs, delivery.ImagePath(seg.URL))
			}
		case llm.SegmentFile:
			if seg.URL != "" {
				outputs = append(outputs, delivery.FilePath(seg.URL))
			}
		}
	}
	for _, platformName := range resolved.Platforms {
		for _, out := range outputs {
			if err := s.send(ctx, delivery.Target{Platform: platformName, Superadmins: true}, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) completeEvent(ctx context.Context, id, resolvedTargets, status, result, eventErr string) error {
	return s.completeEventWithSession(ctx, id, resolvedTargets, status, "", result, eventErr)
}

func (s *Service) completeEventWithSession(ctx context.Context, id, resolvedTargets, status, sessionID, result, eventErr string) error {
	return s.store.ElnisEvents().Update(ctx, storage.UpdateElnisEventRequest{ID: id, ResolvedTargets: resolvedTargets, Status: status, SessionID: sessionID, Result: result, Error: eventErr})
}

func (s *Service) llmPrompt(event Event) string {
	format := strings.TrimSpace(event.Request.Format)
	if format == "" {
		format = "text"
	}
	parts := []string{"[系统 Elnis 后台事件任务]", ""}
	if format == "elyph" {
		parts = append(parts, elyph.RuleCard(), "")
	}
	parts = append(parts,
		"** 按事件内容自主处理，不要把任务当作前台用户对话",
		"** 事件内容来自外部监听器，不需要包含最终 JSON 格式或汇报字段要求",
		"** 信息不足时，在最终 JSON 的 report 填写失败或阻塞原因",
		"** 需要使用工具时直接使用工具",
		"** 有投递目标、任务要求通知或产生需要目标知道的结果/失败/阻塞原因时，应设置 need_report=true 并在 report 写自然语言汇报",
		"** 最终回复必须是严格 JSON",
		"** JSON 格式：{\"completed\":true,\"need_report\":false,\"report\":\"\",\"report_segments\":[]}",
		"** report_segments 可选数组，元素为 {\"type\":\"image|file\",\"url\":\"沙盒绝对路径\"}，用于附带图片或文件。图片/文件须先保存在沙盒内",
		"** 沙盒根目录：" + s.sandboxRoot,
		"** completed 表示是否完成任务",
		"** need_report 表示是否需要向目标平台汇报；成功、失败或阻塞都可以请求汇报",
		"** report 为需要发给目标平台的汇报，可填写处理结果、失败原因或阻塞原因",
		"~ 闲聊",
		"~ 向用户提问",
		"~ 输出 Markdown 代码块",
		"~ 输出 JSON 外的任何文字",
		"",
		"事件标题："+strings.TrimSpace(event.Request.Title),
		"事件来源："+event.Request.Source,
		"事件 ID："+event.Request.ID,
		"事件格式："+format,
	)
	if len(event.Request.Meta) > 0 {
		if data, err := json.Marshal(event.Request.Meta); err == nil {
			parts = append(parts, "事件 metadata："+string(data))
		}
	}
	parts = append(parts, "", "事件内容：", strings.TrimSpace(event.Request.Content))
	return strings.Join(parts, "\n")
}

func (s *Service) modelForEvent(event Event) config.ModelSelection {
	slot := strings.TrimSpace(event.Request.ModelSlot)
	if slot == "" {
		slot = "work"
	}
	if s.resolveModel == nil {
		return config.ModelSelection{}
	}
	selected := s.resolveModel(slot)
	if (selected.Provider == "" || selected.Model == "") && slot != "work" {
		selected = s.resolveModel("work")
	}
	return selected
}

func isElnisModelSlot(slot string) bool {
	switch strings.TrimSpace(slot) {
	case "elwisp1", "elwisp2", "elwisp3":
		return true
	default:
		return false
	}
}

func elnisActor(event Event) security.Actor {
	id := security.ActorID("elnis", event.TokenName)
	return security.Actor{ID: id, Platform: "elnis", PlatformUserID: event.TokenName, DisplayName: event.Request.Elwisp.Name, Role: security.RoleSuperadmin}
}

func firstPlatform(rawTargets string) string {
	var targets Targets
	if err := json.Unmarshal([]byte(rawTargets), &targets); err != nil {
		return ""
	}
	if len(targets.Platforms) == 0 {
		return ""
	}
	return targets.Platforms[0]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}



// --- segment download and output helpers ---

func (s *Service) downloadSegments(ctx context.Context, elwispName, eventID string, segments []Segment) (map[string]string, error) {
	if len(segments) == 0 {
		return nil, nil
	}
	dir := filepath.Join(s.sandboxRoot, "elnis", sanitizeName(elwispName), sanitizeName(eventID))
	paths := make(map[string]string, len(segments))
	for i, seg := range segments {
		if seg.Kind == SegmentKindText {
			continue
		}
		if err := validateSegmentURL(seg.URL); err != nil {
			return nil, fmt.Errorf("segment %d: %w", i, err)
		}
		resolvedPath, err := s.resolveSegment(ctx, dir, seg)
		if err != nil {
			return nil, fmt.Errorf("segment %d: %w", i, err)
		}
		key := segKey(i, seg)
		paths[key] = resolvedPath
	}
	return paths, nil
}

func validateSegmentURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("url is required for image/file segments")
	}
	if strings.HasPrefix(rawURL, "data:") {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https, got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("url host is empty")
	}
	return nil
}

func (s *Service) resolveSegment(ctx context.Context, dir string, seg Segment) (string, error) {
	maxBytes := s.cfg.Segment.MaxFileBytes
	timeout := time.Duration(s.cfg.Segment.DownloadTimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	if strings.HasPrefix(seg.URL, "data:") {
		return s.decodeDataURI(dir, seg, maxBytes)
	}
	return s.downloadURL(ctx, dir, seg, maxBytes, timeout)
}

func (s *Service) decodeDataURI(dir string, seg Segment, maxBytes int64) (string, error) {
	// data:[<mediatype>][;base64],<data>
	raw := strings.TrimSpace(seg.URL)
	if !strings.HasPrefix(raw, "data:") {
		return "", fmt.Errorf("not a data URI")
	}
	commaIdx := strings.Index(raw, ",")
	if commaIdx < 0 {
		return "", fmt.Errorf("invalid data URI: missing comma")
	}
	header := raw[5:commaIdx]
	data := raw[commaIdx+1:]
	isBase64 := strings.HasSuffix(header, ";base64")

	var decoded []byte
	var err error
	if isBase64 {
		decoded, err = base64.StdEncoding.DecodeString(data)
	} else {
		return "", fmt.Errorf("data URI without base64 encoding is not supported")
	}
	if err != nil {
		return "", fmt.Errorf("base64 decode failed: %w", err)
	}
	if maxBytes > 0 && int64(len(decoded)) > maxBytes {
		return "", fmt.Errorf("decoded data size %d exceeds max_file_bytes %d", len(decoded), maxBytes)
	}

	ext := filepath.Ext(strings.TrimSpace(seg.Name))
	if ext == "" && isBase64 {
		mediaType := strings.TrimSuffix(header, ";base64")
		if exts, _ := mime.ExtensionsByType(mediaType); len(exts) > 0 {
			ext = exts[0]
		}
	}
	if ext == "" {
		if seg.Kind == SegmentKindImage {
			ext = ".png"
		} else {
			ext = ".bin"
		}
	}

	name := strings.TrimSpace(seg.Name)
	if name == "" {
		name = storage.NewID()
	}
	filename := name + ext
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create segment dir: %w", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, decoded, 0644); err != nil {
		return "", fmt.Errorf("write segment file: %w", err)
	}
	return path, nil
}

func (s *Service) downloadURL(ctx context.Context, dir string, seg Segment, maxBytes int64, timeout time.Duration) (string, error) {
	dlCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// HEAD to check size
	headReq, err := http.NewRequestWithContext(dlCtx, http.MethodHead, seg.URL, nil)
	if err != nil {
		return "", fmt.Errorf("create HEAD request: %w", err)
	}
	headResp, err := http.DefaultClient.Do(headReq)
	if err != nil {
		return "", fmt.Errorf("HEAD request failed: %w", err)
	}
	headResp.Body.Close()
	if headResp.ContentLength > 0 && maxBytes > 0 && headResp.ContentLength > maxBytes {
		return "", fmt.Errorf("file size %d exceeds max_file_bytes %d", headResp.ContentLength, maxBytes)
	}

	// GET to download
	getReq, err := http.NewRequestWithContext(dlCtx, http.MethodGet, seg.URL, nil)
	if err != nil {
		return "", fmt.Errorf("create GET request: %w", err)
	}
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		return "", fmt.Errorf("GET request failed: %w", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode < 200 || getResp.StatusCode >= 300 {
		return "", fmt.Errorf("download returned HTTP %d", getResp.StatusCode)
	}

	var limitReader io.Reader = getResp.Body
	if maxBytes > 0 {
		limitReader = io.LimitReader(getResp.Body, maxBytes+1)
	}
	data, err := io.ReadAll(limitReader)
	if err != nil {
		return "", fmt.Errorf("read download body: %w", err)
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return "", fmt.Errorf("downloaded size %d exceeds max_file_bytes %d", len(data), maxBytes)
	}

	// Determine filename
	name := strings.TrimSpace(seg.Name)
	if name == "" {
		name = storage.NewID()
		if seg.Kind == SegmentKindImage {
			name += ".png"
		} else {
			name += ".bin"
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create segment dir: %w", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("write segment file: %w", err)
	}
	return path, nil
}

func segKey(i int, seg Segment) string {
	return fmt.Sprintf("%d:%s", i, seg.Kind)
}

func sanitizeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
	if name == "" {
		return "_"
	}
	return name
}

func segmentsContentText(segments []Segment) string {
	var b strings.Builder
	for _, seg := range segments {
		switch seg.Kind {
		case SegmentKindText:
			b.WriteString(seg.Text)
		case SegmentKindImage:
			label := firstNonEmptyStr(seg.Name, seg.URL)
			if label == "" {
				label = "[图片]"
			}
			b.WriteString(fmt.Sprintf("[图片: %s]", label))
		case SegmentKindFile:
			label := firstNonEmptyStr(seg.Name, seg.URL)
			if label == "" {
				label = "[文件]"
			}
			b.WriteString(fmt.Sprintf("[文件: %s]", label))
		}
	}
	return strings.TrimSpace(b.String())
}

func segmentsOutputs(segments []Segment, paths map[string]string) []delivery.Output {
	var out []delivery.Output
	for i, seg := range segments {
		switch seg.Kind {
		case SegmentKindText:
			out = append(out, delivery.Text(seg.Text))
		case SegmentKindImage:
			localPath := pathForSeg(i, seg, paths)
			o := delivery.ImagePath(localPath)
			o.Name = seg.Name
			out = append(out, o)
		case SegmentKindFile:
			localPath := pathForSeg(i, seg, paths)
			o := delivery.FilePath(localPath)
			o.Name = seg.Name
			out = append(out, o)
		}
	}
	return out
}

func segmentsLLM(segments []Segment) []llm.MessageSegment {
	var out []llm.MessageSegment
	for _, seg := range segments {
		switch seg.Kind {
		case SegmentKindText:
			out = append(out, llm.MessageSegment{Type: llm.SegmentText, Text: seg.Text})
		case SegmentKindImage:
			out = append(out, llm.MessageSegment{Type: llm.SegmentImage, URL: seg.URL, Name: seg.Name})
		case SegmentKindFile:
			out = append(out, llm.MessageSegment{Type: llm.SegmentFile, URL: seg.URL, Name: seg.Name})
		}
	}
	return out
}

func pathForSeg(i int, seg Segment, paths map[string]string) string {
	if paths != nil {
		if p, ok := paths[segKey(i, seg)]; ok && p != "" {
			return p
		}
	}
	return seg.URL
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func buildDirectOutputs(req Request, paths map[string]string) []delivery.Output {
	segs := req.Segments
	if len(segs) == 0 {
		return []delivery.Output{delivery.Text(directText(req))}
	}
	out := segmentsOutputs(segs, paths)
	return out
}

func directText(req Request) string {
	parts := []string{}
	if title := strings.TrimSpace(req.Title); title != "" {
		parts = append(parts, title)
	}
	parts = append(parts, strings.TrimSpace(req.Content))
	return strings.Join(parts, "\n")
}

func contentHash(req Request) string {
	data, _ := json.Marshal(req)
	return hashBytes(data)
}

func normalizedToolDeclarations(tools []toolrun.ELwispToolDeclaration) (string, error) {
	if len(tools) == 0 {
		return "", nil
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func hashText(value string) string {
	if value == "" {
		return ""
	}
	return hashBytes([]byte(value))
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func normalizeTargets(targets Targets) Targets {
	return Targets{Platforms: uniqueSorted(targets.Platforms), Superadmins: targets.Superadmins}
}

func hasAllTarget(platforms []string) bool {
	for _, platform := range platforms {
		if strings.EqualFold(strings.TrimSpace(platform), "all") {
			return true
		}
	}
	return false
}

func trimStrings(values []string) []string {
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range trimStrings(values) {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func setFromStrings(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range trimStrings(values) {
		out[value] = true
	}
	return out
}

func backgroundToolNames(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range trimStrings(values) {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func (s *Service) eventAttrs(event Event, attrs ...any) []any {
	attrs = append(attrs,
		"token_name", event.TokenName,
		"elwisp_name", event.Request.Elwisp.Name,
		"source", event.Request.Source,
		"source_id", event.Request.ID,
		"event_key", event.EventKey,
		"mode", event.Request.Mode,
	)
	return attrs
}

func (s *Service) auditEvent(event string, attrs ...any) {
	if s.audit != nil {
		s.audit(event, attrs...)
	}
}

func (s *Service) logInfo(msg string, attrs ...any) {
	if s.logger != nil {
		s.logger.Info(msg, attrs...)
	}
}

func (s *Service) logWarn(msg string, attrs ...any) {
	if s.logger != nil {
		s.logger.Warn(msg, attrs...)
	}
}
