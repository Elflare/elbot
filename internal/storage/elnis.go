package storage

import (
	"context"
	"time"
)

type ElnisEvent struct {
	ID               string
	EventKey         string
	OriginKind       string // 已认证来源类型。
	OriginName       string // 已认证来源名称。
	ElwispName       string
	Source           string
	SourceID         string
	Tags             string
	Mode             string
	ModelSlot        string
	ContentHash      string
	ToolDeclarations string
	ToolHash         string
	RequestedTargets string
	ResolvedTargets  string
	Status           string
	SessionID        string
	Result           string
	Error            string
	ReceivedAt       time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type ElnisReportDelivery struct {
	ID        string
	EventID   string
	Ordinal   int
	Target    string
	Output    string
	MessageID string
	Status    string
	Receipt   string
	Error     string
	Attempts  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

const (
	ElnisReportDeliveryPending   = "pending"
	ElnisReportDeliveryFailed    = "failed"
	ElnisReportDeliveryDelivered = "delivered"
)

type CreateElnisEventRequest struct {
	ID               string // Background 预分配的事件 ID。
	MediaIDs         []string
	EventKey         string
	OriginKind       string // 已认证来源类型。
	OriginName       string // 已认证来源名称。
	ElwispName       string
	Source           string
	SourceID         string
	Tags             string
	Mode             string
	ModelSlot        string
	ContentHash      string
	ToolDeclarations string
	ToolHash         string
	RequestedTargets string
	ResolvedTargets  string
	Status           string
	ReceivedAt       time.Time
	CreatedAt        time.Time
}

type UpdateElnisEventRequest struct {
	ReleaseMediaReferences bool // 与事件更新一并释放事件及其报告的媒体引用。
	ID                     string
	ResolvedTargets        string
	Status                 string
	SessionID              string
	Result                 string
	Error                  string
}

type CreateElnisReportDeliveryRequest struct {
	Target    string
	Output    string
	MessageID string
}

type PrepareElnisReportRequest struct {
	EventID           string
	ResolvedTargets   string
	SessionID         string
	Result            string
	Deliveries        []CreateElnisReportDeliveryRequest
	ResultReadyStatus string
}

type ElnisEventRepository interface {
	// FailInterrupted applies the caller's recovery policy and releases event references atomically.
	FailInterrupted(ctx context.Context, fromStatuses []string, failedStatus, reason string) error
	CreateElnisEvent(ctx context.Context, req CreateElnisEventRequest) (*ElnisEvent, error)
	GetElnisEvent(ctx context.Context, id string) (*ElnisEvent, error)
	GetByKey(ctx context.Context, elwispName, source, sourceID string) (*ElnisEvent, error)
	UpdateElnisEvent(ctx context.Context, req UpdateElnisEventRequest) error
	PrepareReport(ctx context.Context, req PrepareElnisReportRequest) error
	ResetDeliveringReports(ctx context.Context, deliveringStatus, resultReadyStatus string) error
	ListResultReadyReportIDs(ctx context.Context, resultReadyStatus string) ([]string, error)
	ClaimReport(ctx context.Context, eventID, resultReadyStatus, deliveringStatus string) (bool, error)
	ReleaseReport(ctx context.Context, eventID, deliveringStatus, resultReadyStatus, deliveryError string) error
	ListReportDeliveries(ctx context.Context, eventID string) ([]ElnisReportDelivery, error)
	StartReportDelivery(ctx context.Context, id string) error
	// MarkReportDeliveryFailed saves partial receipt mappings and retry state in one transaction.
	MarkReportDeliveryFailed(ctx context.Context, eventID, deliveryID, resultReadyStatus, deliveryError, receipt string, mappings []PlatformMessageMap) error
	// MarkReportDeliveriesDelivered commits one sent batch, its receipt and mappings together.
	MarkReportDeliveriesDelivered(ctx context.Context, deliveryIDs []string, receipt string, mappings []PlatformMessageMap) error
	CompleteReport(ctx context.Context, eventID, deliveringStatus, completedStatus string) error
}
