// result.go 公开统一命令结果和错误契约。
package command

import "elbot/internal/command/share"

type Result = share.Result
type ResultKind = share.ResultKind
type Continuation = share.Continuation
type Error = share.Error
type ErrorKind = share.ErrorKind

const (
	ResultKindText = share.ResultKindText

	ErrorInput       = share.ErrorInput
	ErrorRejected    = share.ErrorRejected
	ErrorInternal    = share.ErrorInternal
	ErrorInterrupted = share.ErrorInterrupted

	CodeUnknownCommand   = share.CodeUnknownCommand
	CodeInvalidArguments = share.CodeInvalidArguments
	CodePermissionDenied = share.CodePermissionDenied
	CodeSessionBusy      = share.CodeSessionBusy
	CodeNotFound         = share.CodeNotFound
	CodeCanceled         = share.CodeCanceled
	CodeTimeout          = share.CodeTimeout
	CodeInternalError    = share.CodeInternalError
)
