package control

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/operationresult"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type requestStub struct {
	active            map[string]request.Request
	cancelled         []string
	cancelledSessions []string
}

func (s *requestStub) List() []request.Request {
	items := []request.Request{}
	for _, item := range s.active {
		items = append(items, item)
	}
	return items
}
func (s *requestStub) Get(id string) (request.Request, bool) {
	item, ok := s.active[id]
	return item, ok
}
func (s *requestStub) Cancel(id string) bool {
	item, ok := s.active[id]
	if !ok {
		return false
	}
	s.cancelled = append(s.cancelled, id)
	item.Stopping = true
	s.active[id] = item
	return true
}
func (s *requestStub) CancelSession(id string) int {
	s.cancelledSessions = append(s.cancelledSessions, id)
	count := 0
	for key, item := range s.active {
		if item.SessionID == id && !item.Stopping {
			item.Stopping = true
			s.active[key] = item
			count++
		}
	}
	return count
}

type requestHistoryStub struct {
	HistoryCapabilities
	rows  map[string]*storage.Session
	err   error
	calls []string
}

func (s *requestHistoryStub) GetSession(_ context.Context, id string) (*storage.Session, error) {
	s.calls = append(s.calls, id)
	if s.err != nil {
		return nil, s.err
	}
	if row := s.rows[id]; row != nil {
		return row, nil
	}
	return nil, storage.ErrNotFound
}

func TestListRequestsPreservesMissingTitlesAndPropagatesReadFailures(t *testing.T) {
	requests := &requestStub{active: map[string]request.Request{
		"empty":   {ID: "empty"},
		"missing": {ID: "missing", SessionID: "deleted"},
		"found":   {ID: "found", SessionID: "session"},
	}}
	history := &requestHistoryStub{rows: map[string]*storage.Session{"session": {ID: "session", Title: "active session"}}}
	m, _ := testSupervisor(t, SupervisorDeps{Requests: requests, History: history})
	listed, err := m.ListRequests(testContext(true))
	if err != nil || listed.Status != operationresult.Success {
		t.Fatalf("list requests: %+v %v", listed, err)
	}
	titles := map[string]string{}
	for _, item := range listed.Data.Items {
		titles[item.ID] = item.SessionTitle
	}
	if len(history.calls) != 2 || titles["empty"] != "" || titles["missing"] != "" || titles["found"] != "active session" {
		t.Fatalf("titles=%v history calls=%v", titles, history.calls)
	}

	failure := errors.New("session store unavailable")
	history.err = failure
	_, err = m.ListRequests(testContext(true))
	if !errors.Is(err, failure) {
		t.Fatalf("session lookup error = %v", err)
	}
}

func TestStopTurnCancelsSessionButLeafStaysLocal(t *testing.T) {
	provider := &requestStub{active: map[string]request.Request{"turn": {ID: "turn", SessionID: "s", Kind: request.KindTurn}, "child": {ID: "child", SessionID: "s", ParentID: "turn"}, "other": {ID: "other", SessionID: "other"}}}
	execution := &executionStub{}
	m, _ := testSupervisor(t, SupervisorDeps{Requests: provider, Execution: execution, Sessions: &sessionStub{}, Scope: func(context.Context) session.Scope { return session.Scope{} }})
	forbidden, err := m.ListRequests(testContext(false))
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, forbidden, operationresult.Forbidden)
	result, err := m.StopRequest(testContext(true), StopRequestRequest{RequestID: "other"})
	if err != nil || result.Status != operationresult.Success || result.Data.AcceptedCount != 1 {
		t.Fatalf("leaf stop: %+v %v", result, err)
	}
	if len(execution.stopped) != 0 || len(provider.cancelledSessions) != 0 {
		t.Fatal("leaf stop cancelled a whole session")
	}
	result, err = m.StopRequest(testContext(true), StopRequestRequest{RequestID: "turn"})
	if err != nil || result.Status != operationresult.Success || result.Data.AcceptedCount != 2 || result.Data.State != "stopping" {
		t.Fatalf("turn stop: %+v %v", result, err)
	}
	if len(execution.stopped) != 1 || execution.stopped[0] != "s" || provider.active["other"].SessionID != "other" {
		t.Fatalf("turn cancellation escaped session: %+v", provider)
	}
	result, err = m.StopRequest(testContext(true), StopRequestRequest{RequestID: "child"})
	if err != nil || result.Status != operationresult.Success || result.Data.AcceptedCount != 0 || result.Data.State != "stopping" {
		t.Fatalf("repeated stop: %+v %v", result, err)
	}
	missing, err := m.StopRequest(testContext(true), StopRequestRequest{RequestID: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, missing, operationresult.NotFound)
}

func TestStopAllUsesDeterministicSessionSet(t *testing.T) {
	provider := &requestStub{active: map[string]request.Request{"a": {ID: "a", SessionID: "s2"}, "b": {ID: "b", SessionID: "s1"}, "c": {ID: "c", SessionID: "s2"}}}
	execution := &executionStub{}
	m, _ := testSupervisor(t, SupervisorDeps{Requests: provider, Execution: execution, Sessions: &sessionStub{}, Scope: func(context.Context) session.Scope { return session.Scope{} }})
	forbidden, err := m.StopAllRequests(testContext(false))
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, forbidden, operationresult.Forbidden)
	result, err := m.StopAllRequests(testContext(true))
	if err != nil || result.Status != operationresult.Success || result.Data.AcceptedCount != 3 || len(result.Data.SessionIDs) != 2 || result.Data.SessionIDs[0] != "s1" || result.Data.SessionIDs[1] != "s2" {
		t.Fatalf("stop all: %+v %v", result, err)
	}
}
