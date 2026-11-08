// result.go 定义 Command 内部共用、由根部公开的结果与错误契约。
package share

import (
	"context"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"encoding/json"
	"errors"
)

// ResultKind 标识 Data 的结构类别，不表示执行状态或命令名称。
type ResultKind string

const (
	ResultKindText      ResultKind = "text"
	ResultKindSession   ResultKind = "session"
	ResultKindSessions  ResultKind = "sessions"
	ResultKindMessages  ResultKind = "messages"
	ResultKindStatus    ResultKind = "status"
	ResultKindModel     ResultKind = "model"
	ResultKindModels    ResultKind = "models"
	ResultKindRequests  ResultKind = "requests"
	ResultKindStop      ResultKind = "stop"
	ResultKindTools     ResultKind = "tools"
	ResultKindRollback  ResultKind = "rollback"
	ResultKindRollbacks ResultKind = "rollbacks"
	ResultKindLogs      ResultKind = "logs"
	ResultKindUsage     ResultKind = "usage"
	ResultKindDoctor    ResultKind = "doctor"
	ResultKindHooks     ResultKind = "hooks"
	ResultKindCompact   ResultKind = "compact"
	ResultKindHelp      ResultKind = "help"
)

// Result 保留唯一结构化业务数据，执行失败通过 error 独立返回。
type Result struct {
	Kind         ResultKind             `json:"kind"`         // 结果的数据类别。
	Data         any                    `json:"data"`         // 对应 Kind 的业务结构体；没有业务数据时为 nil。
	Confirmation *delivery.Confirmation `json:"confirmation"` // 非 nil 表示仍需用户确认，操作尚未执行。
	Continuation *control.Continuation  `json:"-"`            // 结果投递成功后继续处理的输入，不下发给客户端。
}

// UnmarshalJSON 保留原始 JSON，具体业务结构由最终消费端按调用操作解码。
func (r *Result) UnmarshalJSON(data []byte) error {
	var raw struct {
		Kind         ResultKind             `json:"kind"`
		Data         json.RawMessage        `json:"data"`
		Confirmation *delivery.Confirmation `json:"confirmation"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Kind, r.Confirmation, r.Continuation = raw.Kind, raw.Confirmation, nil
	r.Data = nil
	if len(raw.Data) > 0 && string(raw.Data) != "null" {
		r.Data = raw.Data
	}
	return nil
}

// TextData 保存没有其他业务结构的纯文字结果。
type TextData struct {
	Content string `json:"content"` // 纯文字正文。
}

// ErrorKind 区分输入错误、业务拒绝、内部故障和执行中断。
type ErrorKind string

const (
	ErrorInput       ErrorKind = "input"
	ErrorRejected    ErrorKind = "rejected"
	ErrorInternal    ErrorKind = "internal"
	ErrorInterrupted ErrorKind = "interrupted"
)

// 通用错误码保持字符串，业务可以按实际需要扩展具体原因。
const (
	CodeUnknownCommand   = "unknown_command"
	CodeInvalidArguments = "invalid_arguments"
	CodePermissionDenied = "permission_denied"
	CodeSessionBusy      = "session_busy"
	CodeNotFound         = "not_found"
	CodeCanceled         = "canceled"
	CodeTimeout          = "timeout"
	CodeInternalError    = "internal_error"
)

// Error 表达一次命令失败；分类和文案由产生错误的业务边界确定。
type Error struct {
	Kind    ErrorKind `json:"kind"`    // 供展示方统一处理的错误类别。
	Code    string    `json:"code"`    // 具体业务原因，不通过 Message 判断。
	Message string    `json:"message"` // 给用户展示的完整错误说明。
	Cause   error     `json:"-"`       // 原始错误链，仅供 Go 调用方检查和记录。
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

// ClassifyError 在命令执行边界保留已分类错误，并转换公共领域错误。
func ClassifyError(err error) *Error {
	if err == nil {
		return nil
	}
	var failure *Error
	if errors.As(err, &failure) {
		return failure
	}
	var operation *control.Error
	if errors.As(err, &operation) {
		kind := ErrorRejected
		switch operation.Code {
		case CodeInvalidArguments:
			kind = ErrorInput
		case CodeCanceled, CodeTimeout:
			kind = ErrorInterrupted
		case CodeInternalError:
			kind = ErrorInternal
		}
		return &Error{Kind: kind, Code: operation.Code, Message: operation.Message, Cause: operation}
	}
	kind, code, message := ErrorInternal, CodeInternalError, "命令执行失败"
	switch {
	case errors.Is(err, context.Canceled):
		kind, code, message = ErrorInterrupted, CodeCanceled, "命令执行已取消"
	case errors.Is(err, context.DeadlineExceeded):
		kind, code, message = ErrorInterrupted, CodeTimeout, "命令执行超时"
	}
	return &Error{Kind: kind, Code: code, Message: message, Cause: err}
}
