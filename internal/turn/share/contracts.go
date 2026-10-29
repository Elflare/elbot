// 内部数据与等待协作契约。
package share

type RiskAction string

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

type AppendWait struct {
	Refresh <-chan struct{} // A pending refresh; repeated notifications may coalesce.
	Done    <-chan struct{} // Closed by lifecycle when this wait ends.
	Cancel  func() bool     // Stops only this registered wait; false means it is no longer current.
	Expire  func() bool     // Atomically expires this wait unless a pending refresh wins.
}
type RiskWait struct {
	Response <-chan RiskDecision                  // Buffered so a response may precede Wait.
	Refresh  <-chan struct{}                      // Refreshes this wait's deadline.
	Done     <-chan struct{}                      // Closed on stop or completion.
	Complete func(RiskDecision, error) RiskResult // Validates ownership and applies the decision under the lifecycle lock.
	Cancel   func() bool                          // Cancels only this registration.
	Expire   func() bool                          // Atomically expires this wait unless a pending refresh wins.
}
