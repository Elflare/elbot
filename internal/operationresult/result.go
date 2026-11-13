package operationresult

// Status 是 ElBot 内部业务操作状态，不代表任何传输协议状态。
type Status string

const (
	Success          Status = "success"
	Rejected         Status = "rejected"
	Failed           Status = "failed"
	InvalidArguments Status = "invalid_arguments"
	Forbidden        Status = "forbidden"
	NoCurrentSession Status = "no_current_session"
	NotFound         Status = "not_found"
	Conflict         Status = "conflict"
	Unavailable      Status = "unavailable"
	Busy             Status = "busy"
	Timeout          Status = "timeout"
	Canceled         Status = "canceled"
	InvalidTarget    Status = "invalid_target"
	Incompatible     Status = "incompatible"
)

// Result 是业务操作的状态、可展示消息和强类型数据。
// 业务边界仅在 Status 为 Success 时读取 Data；拒绝与失败使用 Message。
type Result[T any] struct {
	Status  Status `json:"status"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}
