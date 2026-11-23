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
	ResultKindQuestions     ResultKind = "questions"
	ResultKindCoagent       ResultKind = "coagent"
	ResultKindCoagentStatus ResultKind = "coagent_status"
	ResultKindQuestionDraft ResultKind = "question_draft"
	ResultKindAnswers       ResultKind = "answers"
)

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
	Status       operationresult.Status  `json:"status"` // Control 业务操作状态。
	Blocks       []delivery.ContentBlock `json:"blocks,omitempty"`
	Kind         ResultKind              `json:"kind,omitempty"` // Data 的结构类别。
	Data         any                     `json:"data,omitempty"` // 对应 Kind 的业务数据；仅成功时读取，可为空。
	Actions      []delivery.Action       `json:"actions,omitempty"`
	Continuation *control.Continuation   `json:"-"` // 结果成功交付后继续处理的输入，不下发给客户端。
}

// FromOperationResult maps a business result to its command result without reading failed data.
func FromOperationResult[T any](result operationresult.Result[T], kind ResultKind, render func(T) string) Result {
	if result.Status != operationresult.Success {
		return TextResult(result.Status, result.Message)
	}
	r := TextResult(result.Status, render(result.Data))
	r.Kind, r.Data = kind, result.Data
	return r
}

// UnmarshalJSON 保留原始 JSON，具体业务结构由最终消费端按调用操作解码。
func (r *Result) UnmarshalJSON(data []byte) error {
	var raw struct {
		Status  operationresult.Status  `json:"status"`
		Blocks  []delivery.ContentBlock `json:"blocks,omitempty"`
		Kind    ResultKind              `json:"kind"`
		Data    json.RawMessage         `json:"data"`
		Actions []delivery.Action       `json:"actions,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Status = raw.Status
	r.Blocks = raw.Blocks
	r.Kind = raw.Kind
	r.Actions = raw.Actions
	r.Continuation = nil
	r.Data = nil
	if raw.Status == operationresult.Success && len(raw.Data) > 0 && string(raw.Data) != "null" {
		r.Data = raw.Data
	}
	return nil
}

func TextResult(status operationresult.Status, text string) Result {
	r := Result{Status: status}
	if text != "" {
		r.Blocks = []delivery.ContentBlock{{Kind: delivery.ContentText, Content: text}}
	}
	return r
}
