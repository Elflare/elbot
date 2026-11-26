package storage

import (
	"context"

	"elbot/internal/storage/maindb"
)

type (
	ElnisEvent                       = maindb.ElnisEvent
	ElnisReportDelivery              = maindb.ElnisReportDelivery
	CreateElnisEventRequest          = maindb.CreateElnisEventRequest
	UpdateElnisEventRequest          = maindb.UpdateElnisEventRequest
	CreateElnisReportDeliveryRequest = maindb.CreateElnisReportDeliveryRequest
	PrepareElnisReportRequest        = maindb.PrepareElnisReportRequest
)

const (
	ElnisReportDeliveryPending   = maindb.ElnisReportDeliveryPending
	ElnisReportDeliveryFailed    = maindb.ElnisReportDeliveryFailed
	ElnisReportDeliveryDelivered = maindb.ElnisReportDeliveryDelivered
)

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
