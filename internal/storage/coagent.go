package storage

import (
	"context"
	"errors"
	"fmt"
)

// MaxCoagents is the total number of member sessions per board, excluding its main session.
const MaxCoagents = 8

var ErrCoagentLimit = fmt.Errorf("每个协作组最多 %d 个 co-agent（主会话另计）", MaxCoagents)

var (
	ErrCoagentNameConflict = errors.New("本组已有同名成员")
	ErrCoagentTarget       = errors.New("目标会话不是本组可选成员")
	ErrCoagentMainOnly     = errors.New("只有主会话可执行此操作")
)

// CoagentMember identifies a currently addressable member on one board.
type CoagentMember struct {
	SessionID string // Current session after any compaction handoff.
	Name      string // Display name assigned when the member joined.
}

type CoagentEntry struct {
	Number          int64  // Board-local sequence, starting at one.
	Author          string // Display name at publication time.
	AuthorSessionID string // Empty for user and system entries.
	TargetSessionID string // Empty for broadcast.
	Text            string
	Pinned          bool // 置顶更新（目标、正文或名册）向全组广播。
}

type CoagentBoard struct {
	ID            int64  // 压缩切换 Session ID 时用此稳定标识校验投递组。
	PinnedText    string // 自由置顶正文，空字符串是有效的内容。
	MainSessionID string
	Goal          string
	Members       []CoagentMember // Includes the main session.
	Entries       []CoagentEntry  // Ordered entries after the requested sequence.
}

type CoagentRepository interface {
	AddMembers(ctx context.Context, main, goal, author string, sessions []*Session) (CoagentBoard, error)
	ReadBoard(ctx context.Context, member string, after int64) (CoagentBoard, error)
	Post(ctx context.Context, member string, user bool, targetSessionID, text string) (CoagentBoard, error)
	RemoveMember(ctx context.Context, member, targetSessionID string, user, retire bool) (CoagentBoard, error)
	Pin(ctx context.Context, member string, user bool, update CoagentPinUpdate) (CoagentBoard, error)
}

// Omitted fields retain their current values. An explicitly empty Text clears the free-form pin.
type CoagentPinUpdate struct {
	Goal *string `json:"goal"`
	Text *string `json:"text"`
}
