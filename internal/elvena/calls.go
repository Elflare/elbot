package elvena

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	CapabilityMessageRecall = "message.recall"
	CapabilityMemberMute    = "member.mute"
	CapabilityChatLeave     = "chat.leave"
)

type PlatformAPICaller interface {
	CallPlatformAPI(ctx context.Context, api string, params map[string]any) (json.RawMessage, error)
}

type PlatformCallerResolver interface {
	PlatformCaller(platform string) (PlatformAPICaller, bool)
}

func ExecuteCalls(ctx context.Context, resolver PlatformCallerResolver, calls []Call) ([]CallResult, error) {
	results := make([]CallResult, 0, len(calls))
	for _, call := range calls {
		result := CallResult{Call: call}
		resp, err := ExecuteCall(ctx, resolver, call)
		if err != nil {
			result.Error = err.Error()
			results = append(results, result)
			return results, err
		}
		result.Response = string(resp)
		results = append(results, result)
	}
	return results, nil
}

func ExecuteCall(ctx context.Context, resolver PlatformCallerResolver, call Call) (json.RawMessage, error) {
	if resolver == nil {
		return nil, fmt.Errorf("platform api caller resolver is not configured")
	}
	raw, err := ResolveRawCall(call)
	if err != nil {
		return nil, err
	}
	platform := strings.TrimSpace(raw.Platform)
	if platform == "" {
		return nil, fmt.Errorf("call platform is required")
	}
	caller, ok := resolver.PlatformCaller(platform)
	if !ok || caller == nil {
		return nil, fmt.Errorf("platform %q does not support api calls", platform)
	}
	return caller.CallPlatformAPI(ctx, raw.API, raw.Params)
}

type RawCall struct {
	Platform string
	API      string
	Params   map[string]any
}

func ResolveRawCall(call Call) (RawCall, error) {
	kind := call.Kind
	if kind == "" {
		switch {
		case strings.TrimSpace(call.Name) != "":
			kind = CallKindCapability
		case strings.TrimSpace(call.API) != "":
			kind = CallKindRaw
		}
	}
	switch kind {
	case CallKindRaw:
		return rawCall(call)
	case CallKindCapability:
		return mapCapability(call)
	default:
		return RawCall{}, fmt.Errorf("unsupported call kind %q", kind)
	}
}

func rawCall(call Call) (RawCall, error) {
	api := strings.TrimSpace(call.API)
	if api == "" {
		return RawCall{}, fmt.Errorf("raw call api is required")
	}
	return RawCall{Platform: callPlatform(call), API: api, Params: cloneParams(call.Params)}, nil
}

func mapCapability(call Call) (RawCall, error) {
	name := strings.TrimSpace(call.Name)
	switch name {
	case CapabilityMessageRecall:
		return mapMessageRecall(call)
	case CapabilityMemberMute:
		return mapMemberMute(call)
	case CapabilityChatLeave:
		return mapChatLeave(call)
	default:
		return RawCall{}, fmt.Errorf("unsupported capability %q", name)
	}
}

func mapMessageRecall(call Call) (RawCall, error) {
	platform := callPlatform(call)
	params := cloneParams(call.Params)
	messageID, err := requiredIntParam(params, "message_id")
	if err != nil {
		return RawCall{}, err
	}
	switch platform {
	case "qq-onebot", "qqonebot":
		return RawCall{Platform: platform, API: "delete_msg", Params: map[string]any{"message_id": messageID}}, nil
	case "telegram":
		chatID, err := targetOrParamID(call.Target, params, "chat_id")
		if err != nil {
			return RawCall{}, err
		}
		return RawCall{Platform: platform, API: "deleteMessage", Params: map[string]any{"chat_id": chatID, "message_id": messageID}}, nil
	default:
		return RawCall{}, fmt.Errorf("capability %s is not mapped for platform %q", CapabilityMessageRecall, platform)
	}
}

func mapMemberMute(call Call) (RawCall, error) {
	platform := callPlatform(call)
	params := cloneParams(call.Params)
	userID, err := requiredIntParam(params, "user_id")
	if err != nil {
		return RawCall{}, err
	}
	duration, err := requiredIntParam(params, "duration_seconds")
	if err != nil {
		return RawCall{}, err
	}
	if duration <= 0 {
		return RawCall{}, fmt.Errorf("duration_seconds must be positive")
	}
	switch platform {
	case "qq-onebot", "qqonebot":
		groupID, err := targetOrParamID(call.Target, params, "group_id")
		if err != nil {
			return RawCall{}, err
		}
		return RawCall{Platform: platform, API: "set_group_ban", Params: map[string]any{"group_id": groupID, "user_id": userID, "duration": duration}}, nil
	case "telegram":
		chatID, err := targetOrParamID(call.Target, params, "chat_id")
		if err != nil {
			return RawCall{}, err
		}
		until := time.Now().Unix() + duration
		permissions := map[string]any{
			"can_send_messages":         false,
			"can_send_audios":           false,
			"can_send_documents":        false,
			"can_send_photos":           false,
			"can_send_videos":           false,
			"can_send_video_notes":      false,
			"can_send_voice_notes":      false,
			"can_send_polls":            false,
			"can_send_other_messages":   false,
			"can_add_web_page_previews": false,
		}
		return RawCall{Platform: platform, API: "restrictChatMember", Params: map[string]any{"chat_id": chatID, "user_id": userID, "until_date": until, "permissions": permissions}}, nil
	default:
		return RawCall{}, fmt.Errorf("capability %s is not mapped for platform %q", CapabilityMemberMute, platform)
	}
}

func mapChatLeave(call Call) (RawCall, error) {
	platform := callPlatform(call)
	params := cloneParams(call.Params)
	switch platform {
	case "qq-onebot", "qqonebot":
		groupID, err := targetOrParamID(call.Target, params, "group_id")
		if err != nil {
			return RawCall{}, err
		}
		out := map[string]any{"group_id": groupID}
		if value, ok := params["is_dismiss"]; ok {
			out["is_dismiss"] = value
		}
		return RawCall{Platform: platform, API: "set_group_leave", Params: out}, nil
	case "telegram":
		chatID, err := targetOrParamID(call.Target, params, "chat_id")
		if err != nil {
			return RawCall{}, err
		}
		return RawCall{Platform: platform, API: "leaveChat", Params: map[string]any{"chat_id": chatID}}, nil
	default:
		return RawCall{}, fmt.Errorf("capability %s is not mapped for platform %q", CapabilityChatLeave, platform)
	}
}

func callPlatform(call Call) string {
	if platform := strings.TrimSpace(call.Platform); platform != "" {
		return platform
	}
	return strings.TrimSpace(call.Target.Platform)
}

func targetOrParamID(target Target, params map[string]any, key string) (int64, error) {
	if target.ID != "" {
		return strconv.ParseInt(strings.TrimSpace(target.ID), 10, 64)
	}
	return requiredIntParam(params, key)
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

func cloneParams(params map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range params {
		out[key] = value
	}
	return out
}
