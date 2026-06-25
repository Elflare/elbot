package elnis

import "elbot/internal/elvena"

const (
	ModeRecord = elvena.ModeRecord
	ModeDirect = elvena.ModeDirect
	ModeLLM    = elvena.ModeLLM

	StatusAccepted    = elvena.StatusAccepted
	StatusQueued      = elvena.StatusQueued
	StatusRunning     = elvena.StatusRunning
	StatusCompleted   = elvena.StatusCompleted
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
