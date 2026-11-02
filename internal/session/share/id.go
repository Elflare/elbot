// 职责：保存 Session ID 的创建来源事实，提供前缀规则及派生继承。
package share

import (
	"encoding/json"
	"fmt"
	"slices"

	"elbot/internal/contextinfo"
	"elbot/internal/storage"
)

const idOriginKey = "id_origin"

// IDOrigin 是创建时的身份快照；接管和投递结果不改写它。
type IDOrigin struct {
	Platform         string                       `json:"platform"`                    // 创建入口的平台。
	ConversationKind contextinfo.ConversationKind `json:"conversation_kind,omitempty"` // CLI、私聊、群聊或频道事实。
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

func (o IDOrigin) Prefix(sessionIDCode func(string, contextinfo.ConversationKind) (string, error)) (string, error) {
	platform := o.Platform
	if o.BackgroundKind != "" && len(o.TargetPlatforms) == 1 {
		platform = o.TargetPlatforms[0]
	}
	code := "mx"
	if o.BackgroundKind == "" || len(o.TargetPlatforms) <= 1 {
		kind := o.ConversationKind
		if o.BackgroundKind != "" {
			kind = contextinfo.ConversationPrivate
		}
		var err error
		code, err = sessionIDCode(platform, kind)
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
	return code, nil
}

func (o IDOrigin) Save(fields storage.SessionMetadata) error {
	return fields.Set(idOriginKey, o)
}

func SessionIDOrigin(row *storage.Session) (IDOrigin, error) {
	fields, err := storage.DecodeSessionMetadata(row.Metadata)
	if err != nil {
		return IDOrigin{}, err
	}
	return IDOriginFromMetadata(fields)
}

// IDOriginFromMetadata 从已解码的元数据读取创建快照，供派生提交复用解析结果。
func IDOriginFromMetadata(fields storage.SessionMetadata) (IDOrigin, error) {
	raw, ok := fields[idOriginKey]
	if !ok {
		return IDOrigin{}, fmt.Errorf("session ID origin is missing")
	}
	var origin IDOrigin
	if err := json.Unmarshal(raw, &origin); err != nil {
		return IDOrigin{}, fmt.Errorf("decode session ID origin: %w", err)
	}
	if origin.Platform == "" {
		return IDOrigin{}, fmt.Errorf("session ID origin platform is missing")
	}
	return origin, nil
}
