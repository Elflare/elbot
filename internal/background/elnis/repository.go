package elnis

import (
	"context"
	"errors"
	"time"

	"elbot/internal/background/share"
)

var ErrEventNotFound = errors.New("elnis event not found")

const ReportDeliveryDelivered = "delivered" // 已持久化成功回执的投递项，无需再次发送。

// EventRecord 是 Elnis 执行和恢复报告所需的快照；JSON 由 Elnis 解释。
type EventRecord struct {
	ID              string // 持久化事件身份。
	EventKey        string // 事件来源的稳定业务标识。
	ContentHash     string // 去重时比较的请求内容摘要。
	ResolvedTargets string // 已解析投递目标的 JSON。
	SessionID       string // 后台执行会话。
	Result          string // 已持久化报告结果的 JSON。
}

type ReportDelivery struct {
	ID        string // 投递项身份，仓储按原始顺序返回。
	Target    string // 单个投递目标的 JSON。
	Output    string // 单个发送内容的 JSON。
	MessageID string // 报告对应的持久化消息。
	Status    string // 当前投递状态。
}

type CreateEventRequest struct {
	MediaIDs         []string  // 创建事件时原子关联的输入媒体。
	EventKey         string    // 事件来源的稳定业务标识。
	TokenName        string    // 已认证来源标签。
	ElwispName       string    // 事件所属 Elwisp。
	Source           string    // Elwisp 内的来源名称。
	SourceID         string    // 来源内的事件标识，与 Elwisp、来源组成去重键。
	Tags             string    // 事件标签的 JSON。
	Mode             string    // 事件执行模式。
	ModelSlot        string    // 使用的模型槽位。
	ContentHash      string    // 请求内容摘要。
	ToolDeclarations string    // 外部工具声明的 JSON。
	ToolHash         string    // 工具声明摘要。
	RequestedTargets string    // 请求目标的 JSON。
	ResolvedTargets  string    // 已解析目标的 JSON。
	Status           string    // 初始执行状态。
	ReceivedAt       time.Time // 宿主接收时间。
	CreatedAt        time.Time // 来源声明的创建时间。
}

type UpdateEventRequest struct {
	ID              string // 要更新的事件身份。
	ResolvedTargets string // 已解析目标的 JSON。
	Status          string // 要提交的执行状态，终态同时释放事件媒体引用。
	SessionID       string // 后台执行会话。
	Result          string // 执行结果。
	Error           string // 执行失败原因。
}

type CreateReportDeliveryRequest struct {
	Target    string // 单个投递目标的 JSON。
	Output    string // 单个发送内容的 JSON。
	MessageID string // 报告对应的持久化消息。
}

type PrepareReportRequest struct {
	EventID           string                        // 报告所属事件。
	ResolvedTargets   string                        // 已解析目标的 JSON。
	SessionID         string                        // 报告所属后台会话。
	Result            string                        // 含稳定媒体 ID 的报告 JSON。
	Deliveries        []CreateReportDeliveryRequest // 按发送顺序持久化的投递项。
	ResultReadyStatus string                        // 报告持久化后可认领的状态。
}

// EventRepository 是 Elnis 独用的持久化契约，由 Background 根部适配。
// 终态和媒体引用清理、报告准备、回执与映射提交均由仓储原子执行。
type EventRepository interface {
	FailInterrupted(context.Context, []string, string, string) error
	Create(context.Context, CreateEventRequest) (*EventRecord, error)
	Get(context.Context, string) (*EventRecord, error)
	GetByKey(context.Context, string, string, string) (*EventRecord, error)
	Update(context.Context, UpdateEventRequest) error
	PrepareReport(context.Context, PrepareReportRequest) error
	ResetDeliveringReports(context.Context, string, string) error
	ListResultReadyReportIDs(context.Context, string) ([]string, error)
	ClaimReport(context.Context, string, string, string) (bool, error)
	ReleaseReport(context.Context, string, string, string, string) error
	ListReportDeliveries(context.Context, string) ([]ReportDelivery, error)
	StartReportDelivery(context.Context, string) error
	MarkReportDeliveryFailed(context.Context, string, string, string, string, string, []share.ReportMessageMap) error
	MarkReportDeliveriesDelivered(context.Context, []string, string, []share.ReportMessageMap) error
	CompleteReport(context.Context, string, string, string) error
}
