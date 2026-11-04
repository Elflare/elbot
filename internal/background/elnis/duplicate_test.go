package elnis

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type concurrentDuplicateRepository struct {
	EventRepository
	reads    sync.WaitGroup
	createMu sync.Mutex
}

func (r *concurrentDuplicateRepository) Create(ctx context.Context, req CreateEventRequest) (*EventRecord, error) {
	r.createMu.Lock()
	defer r.createMu.Unlock()
	if _, err := r.EventRepository.GetByKey(ctx, req.ElwispName, req.Source, req.SourceID); err == nil {
		return nil, errors.New("unique event key")
	}
	return r.EventRepository.Create(ctx, req)
}

func (r *concurrentDuplicateRepository) GetByKey(ctx context.Context, name, source, id string) (*EventRecord, error) {
	row, err := r.EventRepository.GetByKey(ctx, name, source, id)
	if errors.Is(err, ErrEventNotFound) {
		r.reads.Done()
		r.reads.Wait()
	}
	return row, err
}

func TestConcurrentDuplicateAcceptedOnce(t *testing.T) {
	service, cleanup := newTestService(t, nil)
	defer cleanup()
	repo := &concurrentDuplicateRepository{EventRepository: service.store}
	repo.reads.Add(2)
	service.store = repo
	var responses [2]Response
	var errs [2]error
	var workers sync.WaitGroup
	for i := range responses {
		workers.Go(func() {
			responses[i], errs[i] = service.Handle(t.Context(), "secret", testRequest(ModeRecord))
		})
	}
	workers.Wait()
	duplicates := 0
	for i, response := range responses {
		if errs[i] != nil || !response.Accepted {
			t.Fatalf("response=%+v err=%v", response, errs[i])
		}
		if response.Duplicate {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("duplicates=%d", duplicates)
	}
}
