// types.go 引用公共协议类型，并承接 Elnis 内部事件状态。
// TODO：将 Event 从 elvena 迁入本文件，替换当前别名；公共协议类型仍引用 elvena。

package elnis

import "elbot/internal/elvena"

const (
	ModeRecord = elvena.ModeRecord
	ModeDirect = elvena.ModeDirect
	ModeLLM    = elvena.ModeLLM

	StatusAccepted    = elvena.StatusAccepted
	StatusQueued      = elvena.StatusQueued
	StatusRunning     = elvena.StatusRunning
	StatusResultReady = elvena.StatusResultReady
	StatusDelivering  = elvena.StatusDelivering
	StatusCompleted   = elvena.StatusCompleted
	StatusTakenOver   = elvena.StatusTakenOver
	StatusFailed      = elvena.StatusFailed
	StatusDuplicate   = elvena.StatusDuplicate
	StatusUnsupported = elvena.StatusUnsupported
)

type Request = elvena.Request
type Elwisp = elvena.Elwisp
type SegmentKind = elvena.SegmentKind
type Segment = elvena.Segment
type Target = elvena.Target
type Response = elvena.Response
type Call = elvena.Call
type Event = elvena.Event

const (
	SegmentKindText  = elvena.SegmentKindText
	SegmentKindImage = elvena.SegmentKindImage
	SegmentKindFile  = elvena.SegmentKindFile
)
