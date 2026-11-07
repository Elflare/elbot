package delivery

// Confirmation 是命令确认和工具风险确认共用的展示契约。
// 等待、授权和执行由各自业务负责，Action.ID 不是授权凭据。
type Confirmation struct {
	Actions []Action `json:"actions"` // 由业务生成的可选命令操作，展示方直接提交 Command。
}
