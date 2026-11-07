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
	ErrAccessDenied      = errors.New("无权访问该会话")
	ErrSessionBusy       = errors.New("会话正忙")
	ErrForegroundSession = errors.New("会话已由前台接管")
	ErrInvalidPage       = errors.New("invalid session page")
)

// Scope 标识 current 绑定的归属及调用入口的访问范围。
type Scope struct {
	TaskType string                    // 后台任务类型；前台聊天为空。
	TaskID   string                    // 类型内的原生任务 ID。
	ChatType contextinfo.ChatType      // 入口类型，与原生会话 ID 一起参与绑定身份。
	Owner    contextinfo.ActorIdentity // 会话所有者，独立于会话投递平台。
	Platform string                    // 平台标识。
	ChatID   string                    // 平台内的会话范围。
	IsCLI    bool                      // 本地管理入口，可跨用户、平台和范围访问会话。
}

// ScopeKey 仅包含稳定的归属和会话地址，不包含管理权限。
type ScopeKey struct {
	TaskType string // 后台任务类型；前台聊天为空。
	TaskID   string // 类型内的原生任务 ID。
	Owner    contextinfo.ActorIdentity
	Platform string
	ChatType contextinfo.ChatType
	ChatID   string
}

func (s Scope) Key() ScopeKey {
	return ScopeKey{TaskType: s.TaskType, TaskID: s.TaskID, Owner: s.Owner, Platform: s.Platform, ChatType: s.ChatType, ChatID: s.ChatID}
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
	if row.Owner != scope.Owner || row.Platform != scope.Platform {
		return false
	}
	if IsBackground(row) {
		return scope.ChatType == contextinfo.ChatPrivate
	}
	return row.ChatType == scope.ChatType && row.ChatID == scope.ChatID
}

// BackgroundMetadata 设置后台标题的来源，任务身份保存在 Session 独立字段中。
func BackgroundMetadata(fields storage.SessionMetadata, taskType string) error {
	for key, value := range map[string]any{"title_renamed": true, "title_source": taskType} {
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
