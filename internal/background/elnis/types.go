// types.go 引用公共协议类型，并承接 Elnis 内部事件状态。

package elnis

import (
	"time"

	"elbot/internal/elvena"
)

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

// Event 保存规范化后的请求与 Elnis 执行、去重和持久化所需的事实。
type Event struct {
	Request          Request       // 规范化后的公共请求。
	Origin           elvena.Origin // 已交付的调用来源，用于授权和审计。
	EventKey         string        // Elwisp 名称、来源及事件 ID 组成的去重键。
	ContentHash      string        // 请求内容哈希，用于识别同键不同内容。
	ToolDeclarations string        // 外部工具声明的 JSON 快照。
	ToolHash         string        // 工具声明快照的哈希。
	TagsJSON         string        // 规范化标签的 JSON 快照。
	RequestedTargets string        // 请求目标的 JSON 快照。
	ResolvedTargets  string        // 目标裁决结果的 JSON 快照。
	CreatedAt        time.Time     // 请求指定的事件时间，未指定时使用接收阶段时间。
	ReceivedAt       time.Time     // Elnis 完成请求准备时记录的接收时间。
}

const (
	SegmentKindText  = elvena.SegmentKindText
	SegmentKindImage = elvena.SegmentKindImage
	SegmentKindFile  = elvena.SegmentKindFile
)
