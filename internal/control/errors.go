package control

import (
	"context"
	"errors"
	"io/fs"

	"elbot/internal/fileops"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

// Error 由业务边界确定原因；传输适配器只根据 Code 映射状态，不解析 Message。
type Error struct {
	Code    string `json:"code"`    // 稳定的业务原因。
	Message string `json:"message"` // 可直接展示的说明，不包含文件正文或凭据。
	Cause   error  `json:"-"`       // 保留底层错误链，供日志和 errors.Is 使用。
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

// ClassifyError 保留已定性的错误，只转换公开领域错误和标准错误。
func ClassifyError(err error) error {
	if err == nil {
		return nil
	}
	var failure *Error
	if errors.As(err, &failure) {
		return failure
	}
	code, message := "internal_error", "操作执行失败"
	var selection *modelmgr.SelectionError
	switch {
	case errors.Is(err, context.Canceled):
		code, message = "canceled", "操作已取消"
	case errors.Is(err, context.DeadlineExceeded):
		code, message = "timeout", "操作超时"
	case errors.Is(err, session.ErrAccessDenied), errors.Is(err, fs.ErrPermission):
		code, message = "permission_denied", "无权执行该操作"
	case errors.Is(err, session.ErrNoCurrentSession):
		code, message = "no_current_session", "当前没有会话"
	case errors.Is(err, session.ErrSessionBusy):
		code, message = "session_busy", "会话正忙"
	case errors.Is(err, session.ErrChatModeRequiresEmptySession):
		code, message = "mode_conflict", "切换到 chat 模式需要空会话"
	case errors.Is(err, session.ErrInvalidPage):
		code, message = "invalid_arguments", err.Error()
	case errors.Is(err, fileops.ErrFileChanged):
		code, message = "file_changed", err.Error()
	case errors.Is(err, fileops.ErrNotRegularFile):
		code, message = "invalid_target", err.Error()
	case errors.Is(err, tool.ErrToolDetailNotFound), errors.Is(err, tool.ErrSkillNotFound), errors.Is(err, fileops.ErrRollbackNotFound), errors.Is(err, fileops.ErrRollbackExpired), errors.Is(err, storage.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		code, message = "not_found", "目标不存在"
	case errors.Is(err, fs.ErrExist):
		code, message = "file_changed", "目标文件已存在"
	case errors.Is(err, modelmgr.ErrIncompatibleProtocol):
		code, message = "model_incompatible", err.Error()
	case errors.As(err, &selection):
		code, message = "invalid_arguments", selection.Message
	}
	return &Error{Code: code, Message: message, Cause: err}
}

func invalid(message string) *Error { return &Error{Code: "invalid_arguments", Message: message} }
