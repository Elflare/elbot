package contextinfo

import "context"

// Execution describes already established associations, not live state.
// RequestID is the current request; RootRequestID is the main dialogue request.
// RunID spans requests; Attempt identifies one attempt of that execution.
type Execution struct {
	SessionID       string
	RequestID       string
	ParentRequestID string
	RootRequestID   string
	RunID           string
	Attempt         string
}

func RootRequestIDFromContext(ctx context.Context) string {
	execution, _ := ExecutionFromContext(ctx)
	return execution.RootRequestID
}
