// 定义 Turn 的公开输入、快照、执行结果和确认契约。
package turn

import (
	"elbot/internal/llm"
	"elbot/internal/turn/share"
)

type Phase string

const (
	PhaseIdle               Phase = "idle"
	PhaseLLM                Phase = "llm"
	PhaseTool               Phase = "tool"
	PhaseAwaitRiskConfirm   Phase = "awaiting_risk_confirm"
	PhaseAwaitAppendConfirm Phase = "awaiting_append_confirm"
	PhaseCompact            Phase = "compact"
	PhaseStopping           Phase = "stopping"
)

type RiskConfirmation struct {
	ID        string
	ToolName  string
	Arguments string
	Risk      string
	Summary   string
	Detail    string
}

type Snapshot struct {
	SessionID    string
	Phase        Phase
	PendingCount int
	Tools        map[string]int
	// AttemptDone closes when the current attempt has finished its actual work.
	// It is nil when a confirmation is waiting without an active attempt.
	AttemptDone <-chan struct{}
	Execution   *Execution // Logical execution owning this snapshot.
	Attempt     string     // Current attempt; empty during a continuation reservation.
}

type Input struct {
	// NeedsElyphRules travels with queued input until its destination session is known.
	NeedsElyphRules bool
	Text            string
	PlatformText    string
	Segments        []llm.MessageSegment
}

type Result struct {
	RunID     string
	SessionID string
	MessageID string
	Text      string
	TakenOver bool
	Outcome   string
	Err       error
}
type RiskAction string

// AppendWait identifies one registered append confirmation.
// Its private state will move behind the confirmation component during migration.

// RiskConfirmationWait owns one confirmation, including responses received before Wait.
// The caller must wait on it or cancel it after publishing the prompt.

const (
	RiskConfirm     RiskAction = "confirm"
	RiskConfirmTool RiskAction = "confirm_tool"
	RiskConfirmAll  RiskAction = "confirm_all"
	RiskReject      RiskAction = "reject"
)

// RiskDecision records the user's response independently of wait termination.
type RiskDecision struct {
	Action RiskAction // Approval scope or rejection.
	Extra  string     // Additional input supplied with approval.
	Reason string     // Explanation supplied with rejection.
}
type RiskOutcome string

const (
	RiskAnswered RiskOutcome = "answered"
	RiskCanceled RiskOutcome = "canceled"
	RiskExpired  RiskOutcome = "expired"
	RiskStale    RiskOutcome = "stale"
)

// RiskResult separates a user decision from cancellation, expiry and stale ownership.
type RiskResult struct {
	Decision RiskDecision
	Outcome  RiskOutcome
	Err      error // Request cancellation or deadline; nil for user stop or wait expiry.
}

type AppendWait struct{ wait *share.AppendWait }

type RiskConfirmationWait struct{ wait *share.RiskWait }
