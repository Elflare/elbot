package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool"
)

func (w *worker) pluginRequest(value frame) (any, error) {
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
	case "shared.get":
		var params struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, err
		}
		value, ok := w.manager.shared.Get(params.Key)
		return map[string]any{"found": ok, "value": json.RawMessage(value)}, nil
	case "shared.set":
		var params struct {
			Key        string          `json:"key"`
			Value      json.RawMessage `json:"value"`
			TTLSeconds *int64          `json:"ttl_seconds"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, err
		}
		ttl, err := sharedTTL(params.TTLSeconds)
		if err != nil {
			return nil, err
		}
		if err := w.manager.shared.SetWithTTL(params.Key, params.Value, ttl); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "shared.delete":
		var params struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": w.manager.shared.Delete(params.Key)}, nil
	case "shared.list":
		var params struct {
			Prefix string `json:"prefix"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, err
		}
		return map[string]any{"keys": w.manager.shared.List(params.Prefix)}, nil
	case "shared.compare_and_swap":
		var params struct {
			Key        string          `json:"key"`
			Expected   json.RawMessage `json:"expected"`
			Value      json.RawMessage `json:"value"`
			TTLSeconds *int64          `json:"ttl_seconds"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, err
		}
		ttl, err := sharedTTL(params.TTLSeconds)
		if err != nil {
			return nil, err
		}
		swapped, err := w.manager.shared.CompareAndSwapWithTTL(params.Key, params.Expected, params.Value, ttl)
		if err != nil {
			return nil, err
		}
		return map[string]any{"swapped": swapped}, nil
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
	cancel := func() {}
	defer cancel()
	actor := security.Actor{ID: "hook:" + w.config.ID, Role: security.RoleSuperadmin}
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
		actor = security.Actor{ID: contextValue.Event.Actor.ID, Platform: contextValue.Event.Platform.Name, PlatformUserID: contextValue.Event.Actor.UserID, Nickname: contextValue.Event.Actor.Nickname, GroupCard: contextValue.Event.Actor.GroupCard, Role: security.Role(contextValue.Event.Actor.Role), GroupRole: security.GroupRole(contextValue.Event.Actor.GroupRole), DisplayName: contextValue.Event.Actor.DisplayName}
	} else if params.Target.Empty() {
		return nil, fmt.Errorf("background tool output requires an explicit target")
	} else {
		callCtx, cancel = context.WithTimeout(callCtx, time.Duration(w.config.EventTimeoutSeconds)*time.Second)
	}
	callCtx = security.WithActor(callCtx, actor)
	started := time.Now()
	result, err := registered.Call(callCtx, tool.CallRequest{ID: randomID("plugin"), Name: name, Arguments: params.Arguments})
	if w.manager.opts.Audit != nil {
		status := "ok"
		if err != nil {
			status = "error"
		}
		invocation := params.ToolContext
		if params.Background {
			invocation = params.Origin
		}
		w.manager.opts.Audit("hook.tool_call", "hook", w.config.ID, "invocation", invocation, "tool", name, "status", status, "elapsed_ms", time.Since(started).Milliseconds(), "platform", actor.Platform, "user_id", actor.PlatformUserID)
	}
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
