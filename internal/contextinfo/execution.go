package contextinfo

import "context"

// Execution describes already established associations, not live state.
// RequestID is the current request; RootRequestID is the main dialogue request.
// RunID spans requests; Attempt identifies one attempt of that execution.
type Execution struct {
	Mode            string // 已建立的 Session 执行模式；独立工具调用没有此事实。
	TaskType        string // 后台任务类型，只供执行编排和审计使用。
	TaskID          string // 后台任务原生 ID，与聊天地址分离。
	SessionID       string // 未关联该执行事实时为空。
	RequestID       string //不可能空，不要过度判断
	ParentRequestID string // 父请求；无父请求时为空。
	RootRequestID   string //不可能空，不要过度判断
	RunID           string //不可能空，不要过度判断
	Attempt         string //不可能空，不要过度判断
}

func RootRequestIDFromContext(ctx context.Context) string {
	execution, _ := ExecutionFromContext(ctx)
	return execution.RootRequestID
}
