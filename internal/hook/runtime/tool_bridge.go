// 分发插件宿主请求并处理工具调用授权和审计。

package runtime

import "elbot/internal/tool"

import (
	"bytes"
	"context"
	"elbot/internal/hook/share"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
)

func (w *worker) pluginRequest(value frame) (any, error) {
	if strings.HasPrefix(value.Method, "media.") {
		ctx, cancel := context.WithTimeout(w.manager.rootCtx, w.eventTimeout)
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
	arguments := share.TruncateLogContent(string(params.Arguments), 512)
	// Pointer fields distinguish absent operation facts from explicit empty or
	// zero values. Declaration order fixes the log body's display order.
	content := struct {
		Tool               string            `json:"tool,omitempty"`
		Actor              contextinfo.Actor `json:"actor"`
		Arguments          *string           `json:"arguments,omitempty"`
		ElapsedMS          *int64            `json:"elapsed_ms,omitempty"`
		Hook               string            `json:"hook"`
		Invocation         string            `json:"invocation"`
		OutputCount        *int              `json:"output_count,omitempty"`
		PlatformMessageIDs *[]string         `json:"platform_message_ids,omitempty"`
		Summary            *string           `json:"summary,omitempty"` // Source-owned summary, preserved without truncation.
		Result             *string           `json:"result,omitempty"`
		SentMessages       *[]map[string]any `json:"sent_messages,omitempty"`
		Source             string            `json:"source"`
		ToolCallID         string            `json:"tool_call_id,omitempty"`
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
		actor = contextinfo.Actor{ActorIdentity: contextValue.Event.Actor.Identity(), Name: contextValue.Event.Actor.Name, Nickname: contextValue.Event.Actor.Nickname, GroupCard: contextValue.Event.Actor.GroupCard, Role: contextinfo.Role(contextValue.Event.Actor.Role), GroupRole: contextinfo.GroupRole(contextValue.Event.Actor.GroupRole)}
	}
	content.Actor = actor
	logHook := func(eventName string, status globalevents.ResultStatus, body string) {
		content.Result = &body
		if eventName == "hook_tool_call_prepared" {
			content.Result = nil
		}
		if eventName == "hook_tool_call_completed" {
			content.Arguments = nil
		}
		if eventName != "hook_tool_call_completed" {
			body = share.TruncateLogContent(body, 2048)
		}
		data, _ := json.Marshal(content)
		record := globalevents.LogRecord{Category: globalevents.LogCategoryAudit, Level: slog.LevelInfo, EventName: eventName, Module: "hook", Type: globalevents.ContentJSON, Content: string(data), ContentLimit: 0, ResultStatus: status}
		if status == globalevents.ResultFailed {
			record.Level = slog.LevelWarn
		}
		record.Identity = logIdentity(callCtx, sessionID)
		w.manager.opts.Log(callCtx, record)
	}
	failCall := func(status operationresult.Status, message string) (any, error) {
		logHook("hook_tool_call_completed", globalevents.ResultRejected, "Tool.Call not entered: "+message)
		return operationresult.Result[map[string]any]{Status: status, Message: message}, nil
	}
	if err != nil {
		return failCall(operationresult.InvalidArguments, err.Error())
	}
	if name == "" {
		return failCall(operationresult.InvalidArguments, "tool.call requires name")
	}
	call.ID = randomID("plugin")
	content.ToolCallID = call.ID
	allowed := slices.Contains(w.config.Tools.Allow, name)
	if params.Background {
		allowed = slices.Contains(w.config.Tools.BackgroundAllow, name)
	}
	if !allowed {
		return failCall(operationresult.Forbidden, fmt.Sprintf("tool %q is not allowed for hook %q", name, w.config.ID))
	}
	infos, err := w.manager.opts.Tools.QueryTools(callCtx, tool.ToolQuery{Scope: tool.QueryCatalog, Names: []string{name}, IncludeHidden: true, IncludeDiscover: true})
	if err != nil {
		logHook("hook_tool_call_completed", globalevents.ResultFailed, err.Error())
		return nil, err
	}
	if len(infos) == 0 {
		return failCall(operationresult.NotFound, fmt.Sprintf("tool %q not found", name))
	}
	if infos[0].Definition.ForegroundOnly && params.Background {
		return failCall(operationresult.Forbidden, fmt.Sprintf("tool %q is foreground-only", name))
	}
	if !params.Background || strings.TrimSpace(params.Origin) != "" {
		contextValue, ok := w.manager.takeToolContext(invocation, w)
		if !ok {
			return failCall(operationresult.Conflict, "invalid, expired or exhausted hook context")
		}
		callCtx = contextValue.Context
		callCtx = contextinfo.WithActor(callCtx, actor)
	} else {
		// Standalone background execution retains its independent timeout.
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(w.manager.rootCtx, w.eventTimeout)
		defer cancel()
	}
	if err := callCtx.Err(); err != nil {
		status := globalevents.ResultFailed
		if globalevents.LogStatus(err) == globalevents.ResultCanceled {
			status = globalevents.ResultCanceled
		}
		logHook("hook_tool_call_completed", status, err.Error())
		return nil, err
	}
	started := time.Now()
	logHook("hook_tool_call_prepared", globalevents.ResultSucceeded, "")
	callResult, summary, err := w.manager.opts.Tools.CallTool(callCtx, call)
	elapsedMS := time.Since(started).Milliseconds()
	content.ElapsedMS = &elapsedMS
	content.Summary = &summary
	if err != nil {
		status := globalevents.ResultFailed
		if globalevents.LogStatus(err) == globalevents.ResultCanceled {
			status = globalevents.ResultCanceled
		}
		logHook("hook_tool_call_completed", status, err.Error())
		return nil, err
	}
	if callResult.Status != operationresult.Success {
		status := globalevents.ResultRejected
		switch callResult.Status {
		case operationresult.Failed, operationresult.Timeout:
			status = globalevents.ResultFailed
		case operationresult.Canceled:
			status = globalevents.ResultCanceled
		}
		logHook("hook_tool_call_completed", status, callResult.Message)
		return operationresult.Result[map[string]any]{Status: callResult.Status, Message: callResult.Message}, nil
	}
	result := callResult.Data
	logContent := ""
	if result != nil {
		logContent = result.LogContent()
	}
	logHook("hook_tool_call_completed", globalevents.ResultSucceeded, logContent)
	if result == nil {
		return operationresult.Result[map[string]any]{Status: operationresult.Success, Data: map[string]any{"content": "", "segments": []llm.MessageSegment{}, "warnings": []string{}, "receipts": []delivery.Receipt{}}}, nil
	}
	if len(result.Outputs) == 0 {
		return operationresult.Result[map[string]any]{Status: operationresult.Success, Data: map[string]any{"content": result.Content, "segments": result.Segments, "warnings": result.Warnings, "receipts": []delivery.Receipt{}}}, nil
	}
	content.Summary = nil
	content.ElapsedMS = nil
	target := params.Target
	if target.Empty() {
		target, err = delivery.ValidateOutputsTarget(result.Outputs)
	}
	if err != nil {
		logHook("hook_tool_log", globalevents.ResultRejected, err.Error())
		return operationresult.Result[map[string]any]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	sent, err := w.manager.opts.Send(callCtx, target, result.Outputs)
	if err != nil {
		logHook("hook_tool_log", globalevents.ResultFailed, err.Error())
		return nil, err
	}
	if sent.Status != operationresult.Success {
		status := globalevents.ResultRejected
		switch sent.Status {
		case operationresult.Failed, operationresult.Timeout:
			status = globalevents.ResultFailed
		case operationresult.Canceled:
			status = globalevents.ResultCanceled
		}
		logHook("hook_tool_log", status, sent.Message)
		return operationresult.Result[map[string]any]{Status: sent.Status, Message: sent.Message}, nil
	}
	receipt := sent.Data
	content.PlatformMessageIDs = &receipt.PlatformMessageIDs
	sentMessages := []map[string]any{}
	for _, sent := range receipt.SentMessages {
		sentMessages = append(sentMessages, map[string]any{"platform": sent.Platform, "chat_type": sent.ChatType, "chat_id": sent.ChatID, "platform_message_id": sent.PlatformMessageID, "output_indexes": sent.OutputIndexes})
	}
	content.SentMessages = &sentMessages
	outputCount := len(result.Outputs)
	content.OutputCount = &outputCount
	logHook("hook_tool_log", globalevents.ResultSucceeded, "")
	return operationresult.Result[map[string]any]{Status: operationresult.Success, Data: map[string]any{"content": result.Content, "segments": result.Segments, "warnings": result.Warnings, "receipts": []delivery.Receipt{receipt}}}, nil
}

func (w *worker) schemas(ctx context.Context) ([]llm.ToolSchema, error) {
	names := w.config.Tools.Allow
	if len(names) == 0 {
		return []llm.ToolSchema{}, nil
	}
	infos, err := w.manager.opts.Tools.QueryTools(ctx, tool.ToolQuery{Scope: tool.QueryCatalog, Names: names, IncludeHidden: true, IncludeDiscover: true})
	if err != nil {
		return nil, err
	}
	byName := make(map[string]tool.ToolInfo, len(infos))
	for _, info := range infos {
		byName[info.Definition.Name] = info
	}
	out := make([]llm.ToolSchema, 0, len(names))
	for _, name := range names {
		info, ok := byName[name]
		if !ok || info.Parameters == nil {
			return nil, fmt.Errorf("tool %q no longer exists", name)
		}
		out = append(out, llm.ToolSchema{Name: info.Definition.Name, Description: info.Definition.Description, Parameters: info.Parameters})
	}
	return out, nil
}
