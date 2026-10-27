// capability_api.go 将通用能力映射为 OneBot 请求，并提供独立的原生 API 入口。
package qqonebot

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"elbot/internal/platform/share"
)

var (
	_ share.CapabilityCaller  = (*Adapter)(nil)
	_ share.PlatformAPICaller = (*Adapter)(nil)
)

func (a *Adapter) CallCapability(ctx context.Context, name string, params map[string]any) (json.RawMessage, error) {
	switch strings.TrimSpace(name) {
	case "message.recall":
		messageID, err := requiredIntParam(params, "message_id")
		if err != nil {
			return nil, err
		}
		return a.CallPlatformAPI(ctx, "delete_msg", map[string]any{"message_id": messageID})
	case "member.mute":
		userID, err := requiredIntParam(params, "user_id")
		if err != nil {
			return nil, err
		}
		duration, err := requiredIntParam(params, "duration_seconds")
		if err != nil {
			return nil, err
		}
		if duration <= 0 {
			return nil, fmt.Errorf("duration_seconds must be positive")
		}
		groupID, err := requiredIntParam(params, "group_id")
		if err != nil {
			return nil, err
		}
		return a.CallPlatformAPI(ctx, "set_group_ban", map[string]any{"group_id": groupID, "user_id": userID, "duration": duration})
	case "chat.leave":
		groupID, err := requiredIntParam(params, "group_id")
		if err != nil {
			return nil, err
		}
		out := map[string]any{"group_id": groupID}
		if value, ok := params["is_dismiss"]; ok {
			out["is_dismiss"] = value
		}
		return a.CallPlatformAPI(ctx, "set_group_leave", out)
	default:
		return nil, fmt.Errorf("unsupported capability %q", name)
	}
}

func (a *Adapter) CallPlatformAPI(ctx context.Context, api string, params map[string]any) (json.RawMessage, error) {
	resp, err := a.transport.Call(ctx, strings.TrimSpace(api), params)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func requiredIntParam(params map[string]any, key string) (int64, error) {
	value, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	id, ok := toInt64(value)
	if !ok {
		return 0, fmt.Errorf("%s must be integer", key)
	}
	return id, nil
}

func toInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), v == float64(int64(v))
	case json.Number:
		id, err := v.Int64()
		return id, err == nil
	case string:
		id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return id, err == nil
	default:
		return 0, false
	}
}
