package control

import (
	"context"
	"testing"

	"elbot/internal/request"
	"elbot/internal/session"
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

func TestStopTurnCancelsSessionButLeafStaysLocal(t *testing.T) {
	provider := &requestStub{active: map[string]request.Request{"turn": {ID: "turn", SessionID: "s", Kind: request.KindTurn}, "child": {ID: "child", SessionID: "s", ParentID: "turn"}, "other": {ID: "other", SessionID: "other"}}}
	execution := &executionStub{}
	m, _ := testSupervisor(t, SupervisorDeps{Requests: provider, Execution: execution, Sessions: &sessionStub{}, Scope: func(context.Context) session.Scope { return session.Scope{} }})
	_, err := m.ListRequests(testContext(false))
	requireCode(t, err, "permission_denied")
	result, err := m.StopRequest(testContext(true), StopRequestRequest{RequestID: "other"})
	if err != nil || result.AcceptedCount != 1 {
		t.Fatalf("leaf stop: %+v %v", result, err)
	}
	if len(execution.stopped) != 0 || len(provider.cancelledSessions) != 0 {
		t.Fatal("leaf stop cancelled a whole session")
	}
	result, err = m.StopRequest(testContext(true), StopRequestRequest{RequestID: "turn"})
	if err != nil || result.AcceptedCount != 2 || result.State != "stopping" {
		t.Fatalf("turn stop: %+v %v", result, err)
	}
	if len(execution.stopped) != 1 || execution.stopped[0] != "s" || provider.active["other"].SessionID != "other" {
		t.Fatalf("turn cancellation escaped session: %+v", provider)
	}
	result, err = m.StopRequest(testContext(true), StopRequestRequest{RequestID: "child"})
	if err != nil || result.AcceptedCount != 0 || result.State != "stopping" {
		t.Fatalf("repeated stop: %+v %v", result, err)
	}
	_, err = m.StopRequest(testContext(true), StopRequestRequest{RequestID: "missing"})
	requireCode(t, err, "not_found")
}

func TestStopAllUsesDeterministicSessionSet(t *testing.T) {
	provider := &requestStub{active: map[string]request.Request{"a": {ID: "a", SessionID: "s2"}, "b": {ID: "b", SessionID: "s1"}, "c": {ID: "c", SessionID: "s2"}}}
	execution := &executionStub{}
	m, _ := testSupervisor(t, SupervisorDeps{Requests: provider, Execution: execution, Sessions: &sessionStub{}, Scope: func(context.Context) session.Scope { return session.Scope{} }})
	_, err := m.StopAllRequests(testContext(false))
	requireCode(t, err, "permission_denied")
	result, err := m.StopAllRequests(testContext(true))
	if err != nil || result.AcceptedCount != 3 || len(result.SessionIDs) != 2 || result.SessionIDs[0] != "s1" || result.SessionIDs[1] != "s2" {
		t.Fatalf("stop all: %+v %v", result, err)
	}
}
