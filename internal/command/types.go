// types.go 公开命令输入、处理器和补全契约；内部实现通过 share 使用同一份定义。
package command

import (
	"elbot/internal/command/share"
	"elbot/internal/contextinfo"
)

type Info = share.Info
type SessionEffect = share.SessionEffect

const (
	SessionEffectNone          = share.SessionEffectNone
	SessionEffectSwitchCurrent = share.SessionEffectSwitchCurrent
	SessionEffectMutate        = share.SessionEffectMutate
)

type Request = share.Request
type CompletionRequest = share.CompletionRequest
type Completion = share.Completion
type Completer = share.Completer
type Handler = share.Handler

func CanAccess(info Info, actor contextinfo.Actor) bool {
	return share.CanAccess(info, actor)
}
