// 分发插件宿主请求并处理工具调用授权和审计。

package runtime

import "elbot/internal/tool"

import (
	"bytes"
	"context"
	"elbot/internal/hook/share"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
)

func (w *worker) pluginRequest(value frame) (any, error) {
	if strings.HasPrefix(value.Method, "media.") {
		ctx, cancel := context.WithTimeout(w.manager.rootCtx, time.Duration(w.config.EventTimeoutSeconds)*time.Second)
		defer cancel()
		return w.manager.MediaRequest(ctx, w.config.Dir, value.Method, value.Params)
	}
	if strings.HasPrefix(value.Method, "shared.") {
		return w.manager.SharedRequest(value.Method, value.Params)
	}
	switch value.Method {
	case "hooks.reload":
		var params map[string]json.RawMessage
		if len(bytes.TrimSpace(value.Params)) > 0 {
			if err := json.Unmarshal(value.Params, &params); err != nil {
				return nil, err
			}
		}
		if len(params) > 0 {
			return nil, fmt.Errorf("hooks.reload does not accept parameters")
		}
		return w.prepareSelfReload()
	case "tool.call":
		return w.callTool(value.Params)
	default:
		return nil, fmt.Errorf("unsupported hook request method %q", value.Method)
	}
}

func (w *worker) callTool(raw json.RawMessage) (any, error) {
	var params struct {
		Name        string          `json:"name"`
		Arguments   json.RawMessage `json:"arguments"`
		ToolContext string          `json:"tool_context"`
		Origin      string          `json:"origin"`
		Background  bool            `json:"background"`
		Target      delivery.Target `json:"target"`
	}
	err := json.Unmarshal(raw, &params)
	name := strings.TrimSpace(params.Name)
	call := tool.CallRequest{Name: name, Arguments: params.Arguments}
	callCtx := w.manager.rootCtx
	actor, _ := contextinfo.ActorFromContext(callCtx)
	// Keep the trigger actor separate from the permission principal below.
	arguments := share.TruncateLogContent(string(params.Arguments), 512)
	// Pointer fields distinguish absent operation facts from explicit empty or
	// zero values. Declaration order fixes the log body's display order.
	content := struct {
		Tool               string            `json:"tool,omitempty"`
		ActorID            string            `json:"actor_id,omitempty"`
		Arguments          *string           `json:"arguments,omitempty"`
		ElapsedMS          *int64            `json:"elapsed_ms,omitempty"`
		Hook               string            `json:"hook"`
		Invocation         string            `json:"invocation"`
		OutputCount        *int              `json:"output_count,omitempty"`
		Platform           string            `json:"platform,omitempty"`
		PlatformMessageIDs *[]string         `json:"platform_message_ids,omitempty"`
		Result             *string           `json:"result,omitempty"`
		SentMessages       *[]map[string]any `json:"sent_messages,omitempty"`
		Source             string            `json:"source"`
		ToolCallID         string            `json:"tool_call_id,omitempty"`
		UserID             string            `json:"user_id,omitempty"`
	}{
		Tool:      share.TruncateLogContent(name, 2048),
		Arguments: &arguments,
		Hook:      share.TruncateLogContent(w.config.ID, 2048),
		Source:    "plugin",
	}
	invocation := params.ToolContext
	if params.Background {
		invocation = params.Origin
	}
	content.Invocation = share.TruncateLogContent(invocation, 2048)
	// Read trigger facts without consuming the permission token. Guard failures
	// must retain the same trigger identity as an entered call.
	w.manager.mu.RLock()
	contextValue, ok := w.manager.tokens[invocation]
	w.manager.mu.RUnlock()
	sessionID := ""
	if ok && contextValue.Worker == w {
		callCtx = contextValue.Context
		sessionID = contextValue.Event.Session.ID
		if sessionID != "" {
			actor = contextinfo.Actor{ID: contextValue.Event.Actor.ID, Platform: contextValue.Event.Platform.Name, PlatformUserID: contextValue.Event.Actor.UserID}
		} else {
			actor, _ = contextinfo.ActorFromContext(callCtx)
		}
	}
	content.ActorID = share.TruncateLogContent(actor.ID, 2048)
	content.Platform = share.TruncateLogContent(actor.Platform, 2048)
	content.UserID = share.TruncateLogContent(actor.PlatformUserID, 2048)
	logHook := func(eventName string, logResult globalevents.LogResult) {
		content.Result = &logResult.Content
		if eventName == "hook_tool_call_prepared" {
			content.Result = nil
		}
		if eventName == "hook_tool_call_completed" {
			content.Arguments = nil
		}
		if eventName != "hook_tool_call_completed" {
			logResult.Content = share.TruncateLogContent(logResult.Content, 2048)
		}
		data, _ := json.Marshal(content)
		record := globalevents.LogRecord{Category: globalevents.LogAudit, Level: slog.LevelInfo, EventName: eventName, Module: "hook", Type: globalevents.ContentJSON, Content: string(data), ContentLimit: 0, ResultStatus: logResult.ResultStatus}
		if logResult.ResultStatus == globalevents.ResultFailed {
			record.Level = slog.LevelError
		}
		record.Identity = logIdentity(callCtx, sessionID)
		w.manager.opts.Log(callCtx, record)
	}
	failCall := func(status globalevents.ResultStatus, err error) (any, error) {
		logHook("hook_tool_call_completed", globalevents.LogResult{ResultStatus: status, Content: "Tool.Call not entered: " + err.Error()})
		return nil, err
	}
	if err != nil {
		logHook("hook_tool_call_completed", globalevents.LogResult{ResultStatus: globalevents.ResultRejected, Content: "Tool.Call not entered; invalid parameters: " + err.Error()})
		return nil, err
	}
	if name == "" {
		return failCall(globalevents.ResultRejected, fmt.Errorf("tool.call requires name"))
	}
	call.ID = randomID("plugin")
	content.ToolCallID = call.ID
	allowed := contains(w.config.Tools.Allow, name)
	if params.Background {
		allowed = contains(w.config.Tools.BackgroundAllow, name)
	}
	if !allowed {
		return failCall(globalevents.ResultRejected, fmt.Errorf("tool %q is not allowed for hook %q", name, w.config.ID))
	}
	info, ok, err := w.manager.opts.Tools.ToolInfo(callCtx, name)
	if err == nil && !ok {
		err = fmt.Errorf("tool %q not found", name)
	}
	if err != nil {
		return failCall(globalevents.ResultFailed, err)
	}
	if info.ForegroundOnly && params.Background {
		return failCall(globalevents.ResultRejected, fmt.Errorf("tool %q is foreground-only", name))
	}
	// Authorization may use the synthetic Hook principal; audit never does.
	permissionActor := contextinfo.Actor{ID: "hook:" + w.config.ID, Role: contextinfo.RoleSuperadmin}
	if !params.Background || strings.TrimSpace(params.Origin) != "" {
		contextValue, ok := w.manager.takeToolContext(invocation, w)
		if !ok {
			return failCall(globalevents.ResultRejected, fmt.Errorf("invalid, expired or exhausted hook context"))
		}
		callCtx = contextValue.Context
		permissionActor = contextinfo.Actor{ID: contextValue.Event.Actor.ID, Platform: contextValue.Event.Platform.Name, PlatformUserID: contextValue.Event.Actor.UserID, Nickname: contextValue.Event.Actor.Nickname, GroupCard: contextValue.Event.Actor.GroupCard, Role: contextinfo.Role(contextValue.Event.Actor.Role), GroupRole: contextinfo.GroupRole(contextValue.Event.Actor.GroupRole), DisplayName: contextValue.Event.Actor.DisplayName}
	} else if params.Target.Empty() {
		return failCall(globalevents.ResultRejected, fmt.Errorf("background tool output requires an explicit target"))
	} else {
		// Standalone background execution retains its independent timeout.
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(w.manager.rootCtx, time.Duration(w.config.EventTimeoutSeconds)*time.Second)
		defer cancel()
	}
	callCtx = contextinfo.WithActor(callCtx, permissionActor)
	if err := callCtx.Err(); err != nil {
		status := globalevents.ResultFailed
		if err == context.Canceled {
			status = globalevents.ResultCanceled
		}
		return failCall(status, err)
	}
	started := time.Now()
	logHook("hook_tool_call_prepared", globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded})
	result, logContent, err := w.manager.opts.Tools.CallTool(callCtx, call)
	elapsedMS := time.Since(started).Milliseconds()
	content.ElapsedMS = &elapsedMS
	logResult := globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: logContent}
	if err != nil {
		logResult.ResultStatus = globalevents.ResultFailed
		if errors.Is(err, context.Canceled) {
			logResult.ResultStatus = globalevents.ResultCanceled
		}
	}
	logHook("hook_tool_call_completed", logResult)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return map[string]any{"content": "", "segments": []llm.MessageSegment{}, "warnings": []string{}, "receipts": []delivery.Receipt{}}, nil
	}
	if len(result.Outputs) == 0 {
		return map[string]any{"content": result.Content, "segments": result.Segments, "warnings": result.Warnings, "receipts": []delivery.Receipt{}}, nil
	}
	content.Arguments = nil
	content.ElapsedMS = nil
	if !params.Target.Empty() {
		for i := range result.Outputs {
			result.Outputs[i].Target = params.Target
		}
	}
	receipt, logResult, err := w.manager.opts.Send(callCtx, params.Target, result.Outputs)
	content.PlatformMessageIDs = &receipt.PlatformMessageIDs
	sentMessages := []map[string]any{}
	for _, sent := range receipt.SentMessages {
		sentMessages = append(sentMessages, map[string]any{"platform": sent.Platform, "scope_id": sent.ScopeID, "platform_message_id": sent.PlatformMessageID, "output_indexes": sent.OutputIndexes})
	}
	content.SentMessages = &sentMessages
	outputCount := len(result.Outputs)
	content.OutputCount = &outputCount
	logHook("hook_tool_log", logResult)
	if err != nil {
		return nil, err
	}
	return map[string]any{"content": result.Content, "segments": result.Segments, "warnings": result.Warnings, "receipts": []delivery.Receipt{receipt}}, nil
}

func (w *worker) schemas(ctx context.Context) ([]llm.ToolSchema, error) {
	out := make([]llm.ToolSchema, 0, len(w.config.Tools.Allow))
	for _, name := range w.config.Tools.Allow {
		schema, ok, err := w.manager.opts.Tools.ToolSchema(ctx, name)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("tool %q no longer exists", name)
		}
		out = append(out, schema)
	}
	return out, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}
