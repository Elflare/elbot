// result.go 公开统一命令结果契约。
package command

import "elbot/internal/command/share"

type TextData = share.TextData
type Result = share.Result
type ResultKind = share.ResultKind

const (
	ResultKindText      = share.ResultKindText
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
