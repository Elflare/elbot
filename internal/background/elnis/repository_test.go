package elnis

import (
	"context"
	"fmt"
	"sync"
	"testing"

	background "elbot/internal/background/share"
)

// 只记录 Elnis 需要观察的调用结果；SQLite 的原子性由根部集成测试验证。
type fakeEventState struct {
	EventRecord
	Status, Error, ToolDeclarations, ToolHash string
}
type fakeDeliveryState struct {
	ReportDelivery
	Receipt  string
	Attempts int
}
type fakeEventRepository struct {
	mu         sync.Mutex
	events     map[string]fakeEventState
	keys       map[string]string
	deliveries map[string][]fakeDeliveryState
	mappings   []background.ReportMessageMap
	updates    []UpdateEventRequest
	creates    []CreateEventRequest
}

func newFakeEventRepository() *fakeEventRepository {
	return &fakeEventRepository{events: map[string]fakeEventState{}, keys: map[string]string{}, deliveries: map[string][]fakeDeliveryState{}}
}
func testRepo(s *Service) *fakeEventRepository {
	switch r := s.store.(type) {
	case *fakeEventRepository:
		return r
	case *failOnceReceiptRepository:
		return r.EventRepository.(*fakeEventRepository)
	case failCompletedEventRepository:
		return r.EventRepository.(*fakeEventRepository)
	case failingReportReceiptRepository:
		return r.EventRepository.(*fakeEventRepository)
	default:
		panic("unexpected repository fixture")
	}
}
func (r *fakeEventRepository) Create(ctx context.Context, req CreateEventRequest) (*EventRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := fmt.Sprintf("event-%d", len(r.events)+1)
	row := fakeEventState{EventRecord: EventRecord{ID: id, EventKey: req.EventKey, ContentHash: req.ContentHash, ResolvedTargets: req.ResolvedTargets}, Status: req.Status, ToolDeclarations: req.ToolDeclarations, ToolHash: req.ToolHash}
	r.events[id] = row
	r.keys[req.ElwispName+"/"+req.Source+"/"+req.SourceID] = id
	r.creates = append(r.creates, req)
	return &row.EventRecord, nil
}
func (r *fakeEventRepository) snapshot(ctx context.Context, id string) (*fakeEventState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.events[id]
	if !ok {
		return nil, ErrEventNotFound
	}
	return &row, nil
}
func (r *fakeEventRepository) snapshotByKey(ctx context.Context, name, source, id string) (*fakeEventState, error) {
	r.mu.Lock()
	key := r.keys[name+"/"+source+"/"+id]
	r.mu.Unlock()
	return r.snapshot(ctx, key)
}
func (r *fakeEventRepository) Get(ctx context.Context, id string) (*EventRecord, error) {
	row, err := r.snapshot(ctx, id)
	if err != nil {
		return nil, err
	}
	return &row.EventRecord, nil
}
func (r *fakeEventRepository) GetByKey(ctx context.Context, name, source, id string) (*EventRecord, error) {
	row, err := r.snapshotByKey(ctx, name, source, id)
	if err != nil {
		return nil, err
	}
	return &row.EventRecord, nil
}
func (r *fakeEventRepository) Update(ctx context.Context, req UpdateEventRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	row, ok := r.events[req.ID]
	if !ok {
		return ErrEventNotFound
	}
	row.Status = req.Status
	row.Error = req.Error
	row.Result = req.Result
	row.SessionID = req.SessionID
	row.ResolvedTargets = req.ResolvedTargets
	r.events[req.ID] = row
	r.updates = append(r.updates, req)
	return nil
}
func (r *fakeEventRepository) PrepareReport(ctx context.Context, req PrepareReportRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.events[req.EventID]
	row.Status = req.ResultReadyStatus
	row.Result = req.Result
	row.SessionID = req.SessionID
	row.ResolvedTargets = req.ResolvedTargets
	r.events[req.EventID] = row
	for i, item := range req.Deliveries {
		r.deliveries[req.EventID] = append(r.deliveries[req.EventID], fakeDeliveryState{ReportDelivery: ReportDelivery{ID: fmt.Sprintf("%s-%d", req.EventID, i), Target: item.Target, Output: item.Output, MessageID: item.MessageID, Status: "pending"}})
	}
	return nil
}
func (r *fakeEventRepository) ClaimReport(ctx context.Context, id, ready, delivering string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.events[id]
	if row.Status != ready {
		return false, nil
	}
	row.Status = delivering
	r.events[id] = row
	return true, nil
}
func (r *fakeEventRepository) ReleaseReport(ctx context.Context, id, delivering, ready, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.events[id]
	if row.Status == delivering {
		row.Status = ready
		row.Error = reason
		r.events[id] = row
	}
	return nil
}
func (r *fakeEventRepository) deliverySnapshots(ctx context.Context, id string) ([]fakeDeliveryState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]fakeDeliveryState(nil), r.deliveries[id]...), nil
}
func (r *fakeEventRepository) ListReportDeliveries(ctx context.Context, id string) ([]ReportDelivery, error) {
	rows, _ := r.deliverySnapshots(ctx, id)
	out := make([]ReportDelivery, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ReportDelivery)
	}
	return out, nil
}
func (r *fakeEventRepository) StartReportDelivery(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for event, items := range r.deliveries {
		for i := range items {
			if items[i].ID == id {
				items[i].Attempts++
				r.deliveries[event] = items
				return nil
			}
		}
	}
	return ErrEventNotFound
}
func (r *fakeEventRepository) MarkReportDeliveriesDelivered(ctx context.Context, ids []string, receipt string, mappings []background.ReportMessageMap) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for event, items := range r.deliveries {
		for i := range items {
			for _, id := range ids {
				if items[i].ID == id {
					items[i].Status = ReportDeliveryDelivered
					items[i].Receipt = receipt
				}
			}
		}
		r.deliveries[event] = items
	}
	r.mappings = append(r.mappings, mappings...)
	return nil
}
func (r *fakeEventRepository) MarkReportDeliveryFailed(ctx context.Context, eventID, id, ready, reason, receipt string, mappings []background.ReportMessageMap) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := r.deliveries[eventID]
	for i := range items {
		if items[i].ID == id {
			items[i].Status = "failed"
			items[i].Receipt = receipt
		}
	}
	r.deliveries[eventID] = items
	row := r.events[eventID]
	row.Status = ready
	row.Error = reason
	r.events[eventID] = row
	r.mappings = append(r.mappings, mappings...)
	return nil
}
func (r *fakeEventRepository) CompleteReport(ctx context.Context, id, delivering, completed string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.events[id]
	row.Status = completed
	r.events[id] = row
	return nil
}
func (r *fakeEventRepository) ResetDeliveringReports(ctx context.Context, delivering, ready string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, row := range r.events {
		if row.Status == delivering {
			row.Status = ready
			r.events[id] = row
		}
	}
	return nil
}
func (r *fakeEventRepository) ListResultReadyReportIDs(ctx context.Context, ready string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for id, row := range r.events {
		if row.Status == ready {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (r *fakeEventRepository) FailInterrupted(ctx context.Context, statuses []string, failed, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, row := range r.events {
		for _, status := range statuses {
			if row.Status == status {
				row.Status = failed
				row.Error = reason
				r.events[id] = row
			}
		}
	}
	return nil
}
func assertReportMapping(t *testing.T, s *Service, platform, scope, id string) {
	t.Helper()
	repo := testRepo(s)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	want := background.ReportMessageMap{Platform: platform, PlatformScopeID: scope, PlatformMessageID: id, SessionID: "bg-session", MessageID: "bg-message"}
	for _, got := range repo.mappings {
		if got == want {
			return
		}
	}
	t.Fatalf("missing mapping %#v in %#v", want, repo.mappings)
}
