// 职责：定义多个内部组件共用的 Scope、会话事实及判定规则。
// 仅承载实际共用的内部契约和逻辑，不持有组件或运行状态。
package share

import (
	"errors"
	"strings"
	"unicode/utf8"

	"elbot/internal/contextinfo"
	"elbot/internal/storage"
)

var (
	ErrSessionBusy       = errors.New("会话忙")
	ErrForegroundSession = errors.New("会话已由前台接管")
)

// Scope 标识 current 绑定的归属及调用入口的访问范围。
type Scope struct {
	ConversationKind contextinfo.ConversationKind // 私聊、群聊等入口语义，不参与绑定身份。
	ActorID          string                       // 会话所属用户。
	Platform         string                       // 平台标识。
	PlatformScopeID  string                       // 平台内的会话范围。
	IsCLI            bool                         // 本地管理入口，可跨用户、平台和范围访问会话。
}

func (s Scope) Key() string {
	return s.ActorID + "\x00" + s.Platform + "\x00" + s.PlatformScopeID
}

// IsBackground 只读取当前模式；来源、范围名称和接管历史不决定当前身份。
func IsBackground(row *storage.Session) bool {
	return row != nil && row.Mode == storage.SessionModeBackground
}

// WasPromoted 读取已保存的接管标记，不推断或改写当前模式。
func WasPromoted(row *storage.Session) bool {
	if row == nil {
		return false
	}
	fields, err := storage.DecodeSessionMetadata(row.Metadata)
	return err == nil && len(fields["foreground_origin"]) > 0
}

// CanAccess 判断已取得的非 nil 会话是否属于调用入口的访问范围。
func CanAccess(scope Scope, row *storage.Session) bool {
	if scope.IsCLI {
		return true
	}
	if row.OwnerID != scope.ActorID || row.Platform != scope.Platform {
		return false
	}
	if IsBackground(row) {
		return scope.ConversationKind == contextinfo.ConversationPrivate
	}
	return row.PlatformScopeID == scope.PlatformScopeID
}

// BackgroundMetadata 在调用方自有集合中设置后台身份及标题事实，覆盖同名业务字段。
func BackgroundMetadata(fields storage.SessionMetadata, kind, name string) error {
	for key, value := range map[string]any{"title_renamed": true, "title_source": kind, "background_kind": kind, "background_name": name} {
		if err := fields.Set(key, value); err != nil {
			return err
		}
	}
	return nil
}

func BackgroundTitle(kind, name string) string {
	kind, name = strings.TrimSpace(kind), strings.TrimSpace(name)
	if kind == "" {
		kind = "Background"
	}
	_, size := utf8.DecodeRuneInString(kind)
	title := strings.ToUpper(kind[:size]) + kind[size:]
	if name != "" {
		title += ": " + name
	}
	return title
}
