// result.go 公开统一命令结果和错误契约。
package command

import "elbot/internal/command/share"

type TextData = share.TextData
type Result = share.Result
type ResultKind = share.ResultKind
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

const (
	ResultKindSession   = share.ResultKindSession
	ResultKindSessions  = share.ResultKindSessions
	ResultKindMessages  = share.ResultKindMessages
	ResultKindStatus    = share.ResultKindStatus
	ResultKindModel     = share.ResultKindModel
	ResultKindModels    = share.ResultKindModels
	ResultKindRequests  = share.ResultKindRequests
	ResultKindStop      = share.ResultKindStop
	ResultKindTools     = share.ResultKindTools
	ResultKindRollback  = share.ResultKindRollback
	ResultKindRollbacks = share.ResultKindRollbacks
	ResultKindLogs      = share.ResultKindLogs
	ResultKindUsage     = share.ResultKindUsage
	ResultKindDoctor    = share.ResultKindDoctor
	ResultKindHooks     = share.ResultKindHooks
	ResultKindCompact   = share.ResultKindCompact
	ResultKindHelp      = share.ResultKindHelp
)

func ClassifyError(err error) *Error { return share.ClassifyError(err) }
