package runtime

import (
	"context"
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
	"elbot/internal/tool"
)

func (w *worker) pluginRequest(value frame) (any, error) {
	if strings.HasPrefix(value.Method, "media.") {
		ctx, cancel := context.WithTimeout(w.manager.rootCtx, time.Duration(w.config.EventTimeoutSeconds)*time.Second)
		defer cancel()
		return w.manager.MediaRequest(ctx, w.config.Dir, value.Method, value.Params)
	}
	if strings.HasPrefix(value.Method, "shared.") {
		return w.manager.shared.HandleRequest(value.Method, value.Params)
	}
	switch value.Method {
	case "hooks.reload":
		var params map[string]json.RawMessage
		if len(bytesTrim(value.Params)) > 0 {
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
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return nil, fmt.Errorf("tool.call requires name")
	}
	allowed := contains(w.config.Tools.Allow, name)
	if params.Background {
		allowed = contains(w.config.Tools.BackgroundAllow, name)
	}
	if !allowed {
		return nil, fmt.Errorf("tool %q is not allowed for hook %q", name, w.config.ID)
	}
	if w.manager.opts.Registry == nil {
		return nil, fmt.Errorf("tool registry is not configured")
	}
	registered, ok := w.manager.opts.Registry.Get(name)
	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}
	if registered.Info().ForegroundOnly && params.Background {
		return nil, fmt.Errorf("tool %q is foreground-only", name)
	}
	callCtx := context.Background()
	actor := contextinfo.Actor{ID: "hook:" + w.config.ID, Role: contextinfo.RoleSuperadmin}
	if !params.Background || strings.TrimSpace(params.Origin) != "" {
		token := params.ToolContext
		if params.Background {
			token = params.Origin
		}
		contextValue, ok := w.manager.takeToolContext(token, w.config.ID)
		if !ok {
			return nil, fmt.Errorf("invalid, expired or exhausted hook context")
		}
		callCtx = contextValue.Context
		actor = contextinfo.Actor{ID: contextValue.Event.Actor.ID, Platform: contextValue.Event.Platform.Name, PlatformUserID: contextValue.Event.Actor.UserID, Nickname: contextValue.Event.Actor.Nickname, GroupCard: contextValue.Event.Actor.GroupCard, Role: contextinfo.Role(contextValue.Event.Actor.Role), GroupRole: contextinfo.GroupRole(contextValue.Event.Actor.GroupRole), DisplayName: contextValue.Event.Actor.DisplayName}
	} else if params.Target.Empty() {
		return nil, fmt.Errorf("background tool output requires an explicit target")
	} else {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(callCtx, time.Duration(w.config.EventTimeoutSeconds)*time.Second)
		defer cancel()
	}
	callCtx = contextinfo.WithActor(callCtx, actor)
	started := time.Now()
	result, err := registered.Call(callCtx, tool.CallRequest{ID: randomID("plugin"), Name: name, Arguments: params.Arguments})

	level := slog.LevelInfo
	status := "ok"
	if err != nil {
		status = "error"
		level = slog.LevelError
		if errors.Is(err, context.Canceled) {
			level = slog.LevelInfo
			status = "canceled"
		}
	}
	invocation := params.ToolContext
	if params.Background {
		invocation = params.Origin
	}
	_ = globalevents.EmitLog(callCtx, globalevents.LogRecord{
		Category: globalevents.LogAudit,
		Level:    level,
		Name:     "hook_tool_call",
		Module:   "hook",
		Summary:  "hook tool call",
		Fields: []slog.Attr{
			slog.String("source", "plugin"),
			slog.Any("error", err),
			slog.Any("hook", w.config.ID),
			slog.Any("invocation", invocation),
			slog.Any("tool", name),
			slog.Any("status", status),
			slog.Any("elapsed_ms", time.Since(started).Milliseconds()),
			slog.Any("platform", actor.Platform),
			slog.Any("user_id", actor.PlatformUserID),
		},
	})

	if err != nil {
		return nil, err
	}
	if result == nil {
		return map[string]any{"content": "", "segments": []llm.MessageSegment{}, "warnings": []string{}, "receipts": []delivery.Receipt{}}, nil
	}
	if w.manager.opts.Send == nil {
		return nil, fmt.Errorf("hook output sender is not configured")
	}
	if !params.Target.Empty() {
		for i := range result.Outputs {
			result.Outputs[i].Target = params.Target
		}
	}
	receipt, err := w.manager.opts.Send(callCtx, params.Target, result.Outputs)
	if err != nil {
		return nil, err
	}
	return map[string]any{"content": result.Content, "segments": result.Segments, "warnings": result.Warnings, "receipts": []delivery.Receipt{receipt}}, nil
}

func (w *worker) schemas() []llm.ToolSchema {
	if w.manager.opts.Registry == nil {
		return nil
	}
	out := make([]llm.ToolSchema, 0, len(w.config.Tools.Allow))
	for _, name := range w.config.Tools.Allow {
		if registered, ok := w.manager.opts.Registry.Get(name); ok {
			out = append(out, registered.Schema())
		}
	}
	return out
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}
