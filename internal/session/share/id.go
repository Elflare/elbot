// 职责：保存 Session ID 的创建来源事实，提供前缀规则及派生继承。
package share

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/storage"
)

const idOriginKey = "id_origin"

// IDOrigin 是创建时的身份快照；接管和投递结果不改写它。
type IDOrigin struct {
	Platform         string                       `json:"platform"`                    // 创建入口的平台；旧后台会话缺目标快照时也作为目标依据。
	ConversationKind contextinfo.ConversationKind `json:"conversation_kind,omitempty"` // 私聊、群聊或频道事实。
	BackgroundKind   string                       `json:"background_kind,omitempty"`   // cron 或 elnis；空值表示前台入口。
	TargetPlatforms  []string                     `json:"target_platforms,omitempty"`  // 创建时的有效目标平台，排序去重后的快照。
}

func ForegroundIDOrigin(scope Scope) IDOrigin {
	return IDOrigin{Platform: scope.Platform, ConversationKind: scope.ConversationKind}
}

func BackgroundIDOrigin(scope Scope, kind string, platforms []string) IDOrigin {
	platforms = slices.Clone(platforms)
	slices.Sort(platforms)
	return IDOrigin{Platform: scope.Platform, BackgroundKind: kind, TargetPlatforms: slices.Compact(platforms)}
}

func (o IDOrigin) Prefix() (string, error) {
	platform := o.Platform
	if o.BackgroundKind != "" && len(o.TargetPlatforms) == 1 {
		platform = o.TargetPlatforms[0]
	}
	code := "mx"
	if o.BackgroundKind == "" || len(o.TargetPlatforms) <= 1 {
		var err error
		code, err = platformIDCode(platform)
		if err != nil {
			return "", err
		}
	}
	if o.BackgroundKind != "" {
		switch o.BackgroundKind {
		case "cron":
			return "cr" + code, nil
		case "elnis":
			return "ew" + code, nil
		default:
			return "", fmt.Errorf("unknown session background kind %q", o.BackgroundKind)
		}
	}
	if platform != "cli" && (o.ConversationKind == contextinfo.ConversationGroup || o.ConversationKind == contextinfo.ConversationChannel) {
		code = strings.ToUpper(code)
	}
	return code, nil
}

func platformIDCode(platform string) (string, error) {
	switch platform {
	case "qqonebot":
		return "ob", nil
	case "qqofficial":
		return "qq", nil
	case "telegram":
		return "tg", nil
	case "cli":
		return "cl", nil
	default:
		return "", fmt.Errorf("unknown session platform %q", platform)
	}
}

func (o IDOrigin) Save(fields storage.SessionMetadata) error {
	return fields.Set(idOriginKey, o)
}

func SessionIDOrigin(row *storage.Session, scope Scope) (IDOrigin, error) {
	fields, err := storage.DecodeSessionMetadata(row.Metadata)
	if err != nil {
		return IDOrigin{}, err
	}
	return IDOriginFromMetadata(fields, row.Platform, scope)
}

// IDOriginFromMetadata 从已解码的元数据读取创建快照，供派生提交复用解析结果。
func IDOriginFromMetadata(fields storage.SessionMetadata, platform string, scope Scope) (IDOrigin, error) {
	if raw, ok := fields[idOriginKey]; ok {
		var origin IDOrigin
		if err := json.Unmarshal(raw, &origin); err != nil {
			return IDOrigin{}, fmt.Errorf("decode session ID origin: %w", err)
		}
		return origin, nil
	}
	// 旧 UUID 会话没有创建快照，只使用已有归属和当前入口的场景事实。
	origin := ForegroundIDOrigin(scope)
	origin.Platform = platform
	if raw, ok := fields["background_kind"]; ok {
		if err := json.Unmarshal(raw, &origin.BackgroundKind); err != nil {
			return IDOrigin{}, fmt.Errorf("decode session background kind: %w", err)
		}
		origin.TargetPlatforms = []string{platform}
	}
	return origin, nil
}
