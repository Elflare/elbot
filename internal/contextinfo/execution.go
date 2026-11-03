package contextinfo

import "context"

// Execution describes already established associations, not live state.
// RequestID is the current request; RootRequestID is the main dialogue request.
// RunID spans requests; Attempt identifies one attempt of that execution.
type Execution struct {
	SessionID       string //不可能空，不要过度判断
	RequestID       string //不可能空，不要过度判断
	ParentRequestID string //可为空
	RootRequestID   string //不可能空，不要过度判断
	RunID           string //不可能空，不要过度判断
	Attempt         string //不可能空，不要过度判断
}

func RootRequestIDFromContext(ctx context.Context) string {
	execution, _ := ExecutionFromContext(ctx)
	return execution.RootRequestID
}
