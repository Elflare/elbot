// Package format 提供 control 原始业务结果的可选纯文字展示，不执行查询或修改结果。
package format

import (
	"elbot/internal/control"
	"elbot/internal/fileops"
	"fmt"
)

func Model(data control.SelectModelResult) string {
	prefix := "model"
	if data.Target != "current" {
		prefix = data.Target + ".model"
	}
	return fmt.Sprintf("%s → %s/%s", prefix, data.Model.Provider, data.Model.Model)
}
func Stop(data control.StopResult) string {
	if data.State == "stopped" {
		return "stopped"
	}
	return fmt.Sprintf("stopping: %d", data.AcceptedCount)
}
func Rollback(data fileops.RollbackResult) string {
	if data.Deleted {
		return "deleted: " + data.Path
	}
	return fmt.Sprintf("restored: %s · rev=%s", data.Path, data.Revision)
}

// SessionsOptions 只影响文字展示，不改变原始结果。
type SessionsOptions struct {
	MessageLimit int    // 每个会话展示的最近消息数量；0 不展示消息。
	PreviewLimit int    // 每条消息正文的 rune 数量；0 展示全文。
	PrevCommand  string // 上一页操作提示；空字符串不展示。
	NextCommand  string // 下一页操作提示；空字符串不展示。
}
type MessagesOptions struct {
	PreviewLimit int // 每条消息正文的 rune 数量；0 展示全文。
}
type RequestsOptions struct {
	ShowSessionTitle bool // 是否附带请求所属会话标题。
}

func commandUsage(prefix, name, args string) string {
	text := prefix + name
	if args != "" {
		text += " " + args
	}
	return text
}
