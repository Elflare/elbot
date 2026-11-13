// result.go 定义 Command 内部共用、由根部公开的结果契约。
package share

import (
	"encoding/json"

	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
)

// ResultKind 标识 Data 的结构类别，不表示执行状态或命令名称。
type ResultKind string

const (
	ResultKindText      ResultKind = "text"
	ResultKindSession   ResultKind = "session"
	ResultKindSessions  ResultKind = "sessions"
	ResultKindMessages  ResultKind = "messages"
	ResultKindStatus    ResultKind = "status"
	ResultKindModel     ResultKind = "model"
	ResultKindModels    ResultKind = "models"
	ResultKindRequests  ResultKind = "requests"
	ResultKindStop      ResultKind = "stop"
	ResultKindTools     ResultKind = "tools"
	ResultKindRollback  ResultKind = "rollback"
	ResultKindRollbacks ResultKind = "rollbacks"
	ResultKindLogs      ResultKind = "logs"
	ResultKindUsage     ResultKind = "usage"
	ResultKindDoctor    ResultKind = "doctor"
	ResultKindHooks     ResultKind = "hooks"
	ResultKindCompact   ResultKind = "compact"
	ResultKindHelp      ResultKind = "help"
)

// Result 是命令唯一的业务结果载体，状态、展示消息和可选结构化数据统一保存在此。
type Result struct {
	Status       operationresult.Status `json:"status"`                 // Control 业务操作状态。
	Message      string                 `json:"message,omitempty"`      // 拒绝或失败时可直接展示给用户的说明。
	Kind         ResultKind             `json:"kind,omitempty"`         // Data 的结构类别。
	Data         any                    `json:"data,omitempty"`         // 对应 Kind 的业务数据；仅成功时读取，可为空。
	Confirmation *delivery.Confirmation `json:"confirmation,omitempty"` // 非 nil 表示操作等待用户确认。
	Continuation *control.Continuation  `json:"-"`                      // 结果成功交付后继续处理的输入，不下发给客户端。
}

// FromOperationResult maps a business result to its command result without reading failed data.
func FromOperationResult[T any](result operationresult.Result[T], kind ResultKind) Result {
	if result.Status != operationresult.Success {
		return Result{Status: result.Status, Message: result.Message}
	}
	return Result{Status: operationresult.Success, Kind: kind, Data: result.Data}
}

// UnmarshalJSON 保留原始 JSON，具体业务结构由最终消费端按调用操作解码。
func (r *Result) UnmarshalJSON(data []byte) error {
	var raw struct {
		Status       operationresult.Status `json:"status"`
		Message      string                 `json:"message"`
		Kind         ResultKind             `json:"kind"`
		Data         json.RawMessage        `json:"data"`
		Confirmation *delivery.Confirmation `json:"confirmation"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Status = raw.Status
	r.Message = raw.Message
	r.Kind = raw.Kind
	r.Confirmation = raw.Confirmation
	r.Continuation = nil
	r.Data = nil
	if raw.Status == operationresult.Success && len(raw.Data) > 0 && string(raw.Data) != "null" {
		r.Data = raw.Data
	}
	return nil
}

// TextData 保存没有其他业务结构的纯文字成功结果。
type TextData struct {
	Content string `json:"content"` // 纯文字正文。
}
