package control

import (
	"context"
	"errors"
	"slices"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/control/share"
	"elbot/internal/operationresult"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func (m *Supervisor) ListRequests(ctx context.Context) (operationresult.Result[ListRequestsResult], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[ListRequestsResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[ListRequestsResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	result := ListRequestsResult{Items: requestData(m.deps.Requests.List())}
	for i := range result.Items {
		if result.Items[i].SessionID == "" {
			continue
		}
		row, err := m.deps.History.GetSession(ctx, result.Items[i].SessionID)
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return operationresult.Result[ListRequestsResult]{}, err
		}
		result.Items[i].SessionTitle = row.Title
	}
	return operationresult.Result[ListRequestsResult]{Status: operationresult.Success, Data: result}, nil
}

func (m *Supervisor) StopRequest(ctx context.Context, req StopRequestRequest) (operationresult.Result[StopRequestResult], error) {
	result := StopRequestResult{RequestID: req.RequestID, SessionIDs: []string{}, State: "stopped"}
	ready, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[StopRequestResult]{}, err
	}
	if ready.Status != operationresult.Success {
		return operationresult.Result[StopRequestResult]{Status: ready.Status, Message: ready.Message}, nil
	}
	if strings.TrimSpace(req.RequestID) == "" {
		return operationresult.Result[StopRequestResult]{Status: operationresult.InvalidArguments, Message: "request_id 不能为空"}, nil
	}
	var binding *session.Binding
	if ready.Data.Role != contextinfo.RoleSuperadmin {
		current, err := m.deps.Sessions.CurrentBound(ctx, m.deps.Scope(ctx))
		if err != nil {
			return operationresult.Result[StopRequestResult]{}, err
		}
		if current.Status != operationresult.Success {
			return operationresult.Result[StopRequestResult]{Status: current.Status, Message: current.Message}, nil
		}
		bound := current.Data.Binding
		if original, ok := session.BindingFromContext(ctx); ok && (original != bound || !original.Valid()) {
			return operationresult.Result[StopRequestResult]{Status: operationresult.Conflict, Message: "原会话绑定已经失效"}, nil
		}
		binding = bound
	}
	active, ok := m.deps.Requests.Get(req.RequestID)
	if !ok || (binding != nil && active.SessionID != binding.SessionID()) {
		return operationresult.Result[StopRequestResult]{Status: operationresult.NotFound, Message: "请求不存在或不可访问"}, nil
	}
	var release func()
	if binding != nil {
		ctx, release, err = m.deps.Sessions.EnterBinding(ctx, binding)
	} else {
		ctx, release, err = m.deps.Sessions.EnterSessions(ctx, active.SessionID)
	}
	if err != nil {
		return operationresult.Result[StopRequestResult]{}, err
	}
	defer release()
	result.SessionIDs = []string{active.SessionID}
	latest, ok := m.deps.Requests.Get(req.RequestID)
	if !ok || latest.SessionID != active.SessionID {
		return operationresult.Result[StopRequestResult]{Status: operationresult.NotFound, Message: "请求不存在或不可访问"}, nil
	}
	result.State = "stopping"
	if latest.Kind == request.KindTurn {
		m.deps.Execution.StopSession(latest.SessionID)
		result.AcceptedCount = m.deps.Requests.CancelSession(latest.SessionID)
	} else if !latest.Stopping && m.deps.Requests.Cancel(req.RequestID) {
		result.AcceptedCount = 1
	}
	return operationresult.Result[StopRequestResult]{Status: operationresult.Success, Data: result}, nil
}

func (m *Supervisor) StopSession(ctx context.Context) (operationresult.Result[StopSessionResult], error) {
	ready, err := share.Authorize(ctx, false)
	if err != nil {
		return operationresult.Result[StopSessionResult]{}, err
	}
	if ready.Status != operationresult.Success {
		return operationresult.Result[StopSessionResult]{Status: ready.Status, Message: ready.Message}, nil
	}
	current, err := m.deps.Sessions.CurrentBound(ctx, m.deps.Scope(ctx))
	if err != nil {
		return operationresult.Result[StopSessionResult]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[StopSessionResult]{Status: current.Status, Message: current.Message}, nil
	}
	binding := current.Data.Binding
	if original, ok := session.BindingFromContext(ctx); ok && (original != binding || !original.Valid()) {
		return operationresult.Result[StopSessionResult]{Status: operationresult.Conflict, Message: "原会话绑定已经失效"}, nil
	}
	ctx, release, err := m.deps.Sessions.EnterBinding(ctx, binding)
	if err != nil {
		return operationresult.Result[StopSessionResult]{}, err
	}
	defer release()
	m.deps.Execution.StopSession(binding.SessionID())
	return operationresult.Result[StopSessionResult]{Status: operationresult.Success, Data: StopSessionResult{SessionIDs: []string{binding.SessionID()}, AcceptedCount: m.deps.Requests.CancelSession(binding.SessionID()), State: "stopping"}}, nil
}

func (m *Supervisor) StopAllRequests(ctx context.Context) (operationresult.Result[StopAllRequestsResult], error) {
	ready, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[StopAllRequestsResult]{}, err
	}
	if ready.Status != operationresult.Success {
		return operationresult.Result[StopAllRequestsResult]{Status: ready.Status, Message: ready.Message}, nil
	}
	seen := map[string]bool{}
	for _, item := range m.deps.Requests.List() {
		seen[item.SessionID] = true
	}
	for _, item := range m.deps.Execution.SnapshotAll() {
		seen[item.SessionID] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	_, release, err := m.deps.Sessions.EnterSessions(ctx, ids...)
	if err != nil {
		return operationresult.Result[StopAllRequestsResult]{}, err
	}
	defer release()
	result := StopAllRequestsResult{SessionIDs: ids, State: "stopping"}
	for _, id := range ids {
		m.deps.Execution.StopSession(id)
		result.AcceptedCount += m.deps.Requests.CancelSession(id)
	}
	return operationresult.Result[StopAllRequestsResult]{Status: operationresult.Success, Data: result}, nil
}

func requestData(items []request.Request) []RequestData {
	items = slices.Clone(items)
	slices.SortFunc(items, func(a, b request.Request) int {
		if order := a.StartedAt.Compare(b.StartedAt); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	result := make([]RequestData, 0, len(items))
	for _, item := range items {
		result = append(result, RequestData{ID: item.ID, ParentID: item.ParentID, SessionID: item.SessionID, Kind: item.Kind, Label: item.Label, StartedAt: item.StartedAt, Stopping: item.Stopping})
	}
	return result
}
