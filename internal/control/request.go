package control

import (
	"context"
	"slices"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/request"
	"elbot/internal/session"
)

func (m *Supervisor) ListRequests(ctx context.Context) (ListRequestsResult, error) {
	if err := authorize(ctx, true, m.deps.Requests != nil); err != nil {
		return ListRequestsResult{}, err
	}
	result := ListRequestsResult{Items: requestData(m.deps.Requests.List())}
	if m.deps.History != nil {
		for i := range result.Items {
			if row, err := m.deps.History.GetSession(ctx, result.Items[i].SessionID); err == nil {
				result.Items[i].SessionTitle = row.Title
			}
		}
	}
	return result, nil
}

func (m *Supervisor) StopRequest(ctx context.Context, req StopRequestRequest) (StopRequestResult, error) {
	result := StopRequestResult{RequestID: req.RequestID, SessionIDs: []string{}, State: "stopped"}
	if err := m.requestReady(ctx, false); err != nil {
		return result, err
	}
	if strings.TrimSpace(req.RequestID) == "" {
		return result, invalid("request_id 不能为空")
	}
	actor, _ := contextinfo.ActorFromContext(ctx)
	var binding *session.Binding
	if actor.Role != contextinfo.RoleSuperadmin {
		_, bound, err := m.deps.Sessions.CurrentBound(ctx, m.deps.Scope(ctx))
		if err != nil {
			return result, ClassifyError(err)
		}
		if original, ok := session.BindingFromContext(ctx); ok && (original != bound || !original.Valid()) {
			return result, &Error{Code: "binding_changed", Message: "原会话绑定已经失效"}
		}
		binding = bound
	}
	active, ok := m.deps.Requests.Get(req.RequestID)
	if !ok || (binding != nil && active.SessionID != binding.SessionID()) {
		return result, &Error{Code: "not_found", Message: "请求不存在或不可访问"}
	}
	var release func()
	var err error
	if binding != nil {
		ctx, release, err = m.deps.Sessions.EnterBinding(ctx, binding)
	} else {
		ctx, release, err = m.deps.Sessions.EnterSessions(ctx, active.SessionID)
	}
	if err != nil {
		return result, ClassifyError(err)
	}
	defer release()
	result.SessionIDs = []string{active.SessionID}
	latest, ok := m.deps.Requests.Get(req.RequestID)
	if !ok || latest.SessionID != active.SessionID {
		return result, nil
	}
	result.State = "stopping"
	if latest.Kind == request.KindTurn {
		m.deps.Execution.StopSession(latest.SessionID)
		result.AcceptedCount = m.deps.Requests.CancelSession(latest.SessionID)
	} else if !latest.Stopping && m.deps.Requests.Cancel(req.RequestID) {
		result.AcceptedCount = 1
	}
	return result, nil
}

func (m *Supervisor) StopSession(ctx context.Context) (StopSessionResult, error) {
	if err := m.requestReady(ctx, false); err != nil {
		return StopSessionResult{}, err
	}
	_, binding, err := m.deps.Sessions.CurrentBound(ctx, m.deps.Scope(ctx))
	if err != nil {
		return StopSessionResult{}, ClassifyError(err)
	}
	if original, ok := session.BindingFromContext(ctx); ok && (original != binding || !original.Valid()) {
		return StopSessionResult{}, &Error{Code: "binding_changed", Message: "原会话绑定已经失效"}
	}
	ctx, release, err := m.deps.Sessions.EnterBinding(ctx, binding)
	if err != nil {
		return StopSessionResult{}, ClassifyError(err)
	}
	defer release()
	m.deps.Execution.StopSession(binding.SessionID())
	return StopSessionResult{SessionIDs: []string{binding.SessionID()}, AcceptedCount: m.deps.Requests.CancelSession(binding.SessionID()), State: "stopping"}, nil
}

func (m *Supervisor) StopAllRequests(ctx context.Context) (StopAllRequestsResult, error) {
	if err := m.requestReady(ctx, true); err != nil {
		return StopAllRequestsResult{}, err
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
		return StopAllRequestsResult{}, ClassifyError(err)
	}
	defer release()
	result := StopAllRequestsResult{SessionIDs: ids, State: "stopping"}
	for _, id := range ids {
		m.deps.Execution.StopSession(id)
		result.AcceptedCount += m.deps.Requests.CancelSession(id)
	}
	return result, nil
}

func (m *Supervisor) requestReady(ctx context.Context, admin bool) error {
	if err := authorize(ctx, admin, m.deps.Requests != nil); err != nil {
		return err
	}
	if m.deps.Sessions == nil || m.deps.Execution == nil {
		return &Error{Code: "not_configured", Message: "请求停止所需的会话准入尚未接入"}
	}
	return nil
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
