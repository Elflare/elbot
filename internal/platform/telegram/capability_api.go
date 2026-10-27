// capability_api.go 将通用能力映射为 Telegram 请求，并提供独立的原生 API 入口。
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"elbot/internal/platform/share"
)

var (
	_ share.CapabilityCaller  = (*Adapter)(nil)
	_ share.PlatformAPICaller = (*Adapter)(nil)
)

func (a *Adapter) CallCapability(ctx context.Context, name string, params map[string]any) (json.RawMessage, error) {
	switch strings.TrimSpace(name) {
	case "message.recall":
		chatID, err := requiredIntParam(params, "chat_id")
		if err != nil {
			return nil, err
		}
		messageID, err := requiredIntParam(params, "message_id")
		if err != nil {
			return nil, err
		}
		return a.CallPlatformAPI(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID})
	case "member.mute":
		chatID, err := requiredIntParam(params, "chat_id")
		if err != nil {
			return nil, err
		}
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
		permissions := map[string]any{
			"can_send_messages": false, "can_send_audios": false, "can_send_documents": false,
			"can_send_photos": false, "can_send_videos": false, "can_send_video_notes": false,
			"can_send_voice_notes": false, "can_send_polls": false, "can_send_other_messages": false,
			"can_add_web_page_previews": false,
		}
		return a.CallPlatformAPI(ctx, "restrictChatMember", map[string]any{"chat_id": chatID, "user_id": userID, "until_date": time.Now().Unix() + duration, "permissions": permissions})
	case "chat.leave":
		chatID, err := requiredIntParam(params, "chat_id")
		if err != nil {
			return nil, err
		}
		return a.CallPlatformAPI(ctx, "leaveChat", map[string]any{"chat_id": chatID})
	default:
		return nil, fmt.Errorf("unsupported capability %q", name)
	}
}

func (a *Adapter) CallPlatformAPI(ctx context.Context, api string, params map[string]any) (json.RawMessage, error) {
	ctx, finish, err := a.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	return a.client.callRaw(ctx, strings.TrimSpace(api), params)
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
