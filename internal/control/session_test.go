package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type historyStub struct {
	account  contextinfo.ActorIdentity
	limit    int
	row      *storage.Session
	reads    int
	messages []storage.Message
	page     int
	pageSize int
}

func (s *historyStub) GetSession(context.Context, string) (*storage.Session, error) {
	return s.row, nil
}
func (s *historyStub) LoadMessagePage(_ context.Context, _ string, page, pageSize int) (contextmgr.MessagePage, error) {
	s.reads++
	s.page, s.pageSize = page, pageSize
	return contextmgr.MessagePage{TotalPages: 1, Messages: s.messages}, nil
}

func (s *historyStub) ListInputHistory(_ context.Context, account contextinfo.ActorIdentity, limit int) ([]string, error) {
	s.account, s.limit = account, limit
	return []string{"hello"}, nil
}

func TestListInputHistoryUsesAuthenticatedAccount(t *testing.T) {
	provider := &historyStub{}
	m, _ := testSupervisor(t, SupervisorDeps{AgentDeps: AgentDeps{History: provider}})
	denied, err := m.ListInputHistory(context.Background())
	if err != nil || denied.Status != operationresult.Forbidden || provider.limit != 0 {
		t.Fatal(denied, err, provider.limit)
	}
	for _, admin := range []bool{false, true} {
		provider.account, provider.limit = contextinfo.ActorIdentity{}, 0
		result, err := m.ListInputHistory(testContext(admin))
		if err != nil || result.Status != operationresult.Success || !reflect.DeepEqual(result.Data, []string{"hello"}) || provider.account != (contextinfo.ActorIdentity{Platform: "cli", ID: "test"}) || provider.limit != 200 {
			t.Fatal(result, err, provider.account, provider.limit)
		}
	}
}
func TestHistoryProjectsInputIDs(t *testing.T) {
	rows, err := messageData([]storage.Message{{Role: llm.RoleUser, Metadata: `{"input_ids":["a","b"],"elyph_rules_shown":true}`}, {Role: llm.RoleUser}})
	if err != nil || !reflect.DeepEqual(rows[0].InputIDs, []string{"a", "b"}) || rows[1].InputIDs != nil {
		t.Fatal(rows, err)
	}
}

func TestHistoryChecksScopeAndPreservesMessages(t *testing.T) {
	provider := &historyStub{row: &storage.Session{ID: "s", Owner: contextinfo.ActorIdentity{Platform: "qq", ID: "other"}, Platform: "qq", ChatType: contextinfo.ChatPrivate, ChatID: "chat"}}
	segments := []llm.MessageSegment{
		{Type: llm.SegmentText, Text: "before\n"},
		{Type: llm.SegmentFile, Name: "a.txt", MediaID: "media:file", MIMEType: "text/plain"},
		{Type: llm.SegmentText, Text: "after"},
		{Type: llm.SegmentImage, MediaID: "media:image", MIMEType: "image/png", URL: "https://example.com/image.png"},
	}
	raw, err := json.Marshal(segments)
	if err != nil {
		t.Fatal(err)
	}
	message := storage.Message{ID: "m", SessionID: "s", Role: "assistant", Content: "exact\n", SourceContent: "model source", ReasoningContent: "thought", ParentMessageID: "parent", ReplyToPlatformMessageID: "platform-message", ReplyToMessageID: "reply", ToolCallID: "tool-call", ToolCalls: []llm.ToolCallRequest{{ID: "call-1", Name: "shell", Arguments: `{"command":"pwd"}`}}, ToolBatchStatus: "completed", Metadata: `{"internal":"must not be returned"}`, Segments: string(raw), CreatedAt: time.Now()}
	provider.messages = []storage.Message{message}
	m, _ := testSupervisor(t, SupervisorDeps{AgentDeps: AgentDeps{History: provider, Scope: func(context.Context) session.Scope {
		return session.Scope{Platform: "qq", ChatType: contextinfo.ChatPrivate, ChatID: "chat", Owner: contextinfo.ActorIdentity{Platform: "qq", ID: "user"}}
	}}})
	forbidden, err := m.SessionMessages(testContext(false), SessionMessagesRequest{SessionID: "s", Page: 1, PageSize: 250})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, forbidden, operationresult.Forbidden)
	if provider.reads != 0 {
		t.Fatal("foreign session history loaded before access check")
	}
	provider.row.Owner.ID = "user"
	result, err := m.SessionMessages(testContext(false), SessionMessagesRequest{SessionID: "s", Page: 1, PageSize: 250})
	if err != nil {
		t.Fatal(err)
	}
	want := MessageData{ID: message.ID, SessionID: message.SessionID, Role: string(message.Role), Content: message.Content, SourceContent: message.SourceContent, ReasoningContent: message.ReasoningContent, ParentMessageID: message.ParentMessageID, ReplyToPlatformMessageID: message.ReplyToPlatformMessageID, ReplyToMessageID: message.ReplyToMessageID, ToolCallID: message.ToolCallID, Segments: segments, ToolCalls: []ToolCallData{{ID: "call-1", Name: "shell", Arguments: `{"command":"pwd"}`}}, ToolBatchStatus: message.ToolBatchStatus, CreatedAt: message.CreatedAt}
	if result.Status != operationresult.Success || !reflect.DeepEqual(result.Data.Items, []MessageData{want}) {
		t.Fatalf("history data: %+v", result)
	}
	encoded, err := json.Marshal(result.Data.Items[0])
	var fields map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(encoded, &fields)
	}
	if _, hasMetadata := fields["metadata"]; err != nil || hasMetadata {
		t.Fatalf("history JSON = %s, err=%v", encoded, err)
	}
	var gotCalls []ToolCallData
	if err := json.Unmarshal(fields["tool_calls"], &gotCalls); err != nil || !reflect.DeepEqual(gotCalls, want.ToolCalls) {
		t.Fatalf("tool calls JSON = %s, parsed=%+v, err=%v", fields["tool_calls"], gotCalls, err)
	}
	if provider.page != 1 || provider.pageSize != 250 || result.Data.PageSize != 250 {
		t.Fatal("caller-selected pagination was changed")
	}
	for _, req := range []SessionMessagesRequest{
		{SessionID: "s", Page: 2, PageSize: 250},
		{SessionID: "s", Page: 1},
	} {
		invalid, err := m.SessionMessages(testContext(false), req)
		if err != nil {
			t.Fatal(err)
		}
		requireStatus(t, invalid, operationresult.InvalidArguments)
	}
}

type managedSessionStub struct {
	historyStub
	sessionStub
	target        string
	title         string
	failure       error
	failureStatus operationresult.Status
	rows          map[string]*storage.Session
	listed        []storage.SessionSummary
	queries       []session.ListRequest
}

func (*managedSessionStub) CurrentBound(context.Context, session.Scope) (operationresult.Result[session.CurrentBoundData], error) {
	panic("management operation inspected the current binding")
}
func (s *managedSessionStub) Rename(_ context.Context, _ session.Scope, id, title string) (operationresult.Result[*storage.Session], error) {
	s.target, s.title = id, title
	if s.failure != nil {
		return operationresult.Result[*storage.Session]{}, s.failure
	}
	if s.failureStatus != "" && s.failureStatus != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: s.failureStatus, Message: "session operation rejected"}, nil
	}
	return operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: &storage.Session{ID: id, Title: title}}, nil
}
func (s *managedSessionStub) GetSession(_ context.Context, id string) (*storage.Session, error) {
	if row := s.rows[id]; row != nil {
		return row, nil
	}
	return nil, storage.ErrNotFound
}
func (*managedSessionStub) LoadMessagePage(context.Context, string, int, int) (contextmgr.MessagePage, error) {
	return contextmgr.MessagePage{}, nil
}
func (s *managedSessionStub) ListPage(_ context.Context, _ session.Scope, req session.ListRequest) (operationresult.Result[session.ListResult], error) {
	s.queries = append(s.queries, req)
	filtered := make([]storage.SessionSummary, 0, len(s.listed))
	for _, row := range s.listed {
		if strings.Contains(row.Title, req.Query) {
			filtered = append(filtered, row)
		}
	}
	start := (req.Page - 1) * req.PageSize
	end := min(start+req.PageSize, len(filtered))
	items := []storage.SessionSummary{}
	if start < end {
		items = filtered[start:end]
	}
	total := len(filtered)
	pages := total / req.PageSize
	if total%req.PageSize != 0 {
		pages++
	}
	return operationresult.Result[session.ListResult]{Status: operationresult.Success, Data: session.ListResult{Items: items, Page: req.Page, PageSize: req.PageSize, TotalPages: pages}}, nil
}
func (s *managedSessionStub) Archive(ctx context.Context, scope session.Scope, id string) (operationresult.Result[*storage.Session], error) {
	return s.Rename(ctx, scope, id, "")
}
func (s *managedSessionStub) Unarchive(ctx context.Context, scope session.Scope, id string) (operationresult.Result[*storage.Session], error) {
	return s.Rename(ctx, scope, id, "")
}
func (s *managedSessionStub) Pin(ctx context.Context, scope session.Scope, id string) (operationresult.Result[*storage.Session], error) {
	return s.Rename(ctx, scope, id, "")
}
func (s *managedSessionStub) Unpin(ctx context.Context, scope session.Scope, id string) (operationresult.Result[*storage.Session], error) {
	return s.Rename(ctx, scope, id, "")
}
func (s *managedSessionStub) Delete(_ context.Context, _ session.Scope, id string) (operationresult.Result[struct{}], error) {
	s.target = id
	if s.failure != nil {
		return operationresult.Result[struct{}]{}, s.failure
	}
	if s.failureStatus != "" && s.failureStatus != operationresult.Success {
		return operationresult.Result[struct{}]{Status: s.failureStatus, Message: "session operation rejected"}, nil
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func TestSessionManagementUsesExplicitTargetAndProviderAdmission(t *testing.T) {
	provider := &managedSessionStub{sessionStub: sessionStub{current: &storage.Session{ID: "current"}}, rows: map[string]*storage.Session{"target": {ID: "target"}}}
	m, _ := testSupervisor(t, SupervisorDeps{AgentDeps: AgentDeps{Sessions: provider, History: provider, Scope: func(context.Context) session.Scope { return session.Scope{} }}})
	operations := map[string]func() (operationresult.Status, error){
		"rename": func() (operationresult.Status, error) {
			result, err := m.RenameSession(testContext(false), RenameSessionRequest{Target: "target", Title: "title", PageSize: 10})
			return result.Status, err
		},
		"archive": func() (operationresult.Status, error) {
			result, err := m.SetSessionArchived(testContext(false), SetSessionArchivedRequest{SessionID: "target", Archived: true, Confirmed: true})
			return result.Status, err
		},
		"unarchive": func() (operationresult.Status, error) {
			result, err := m.SetSessionArchived(testContext(false), SetSessionArchivedRequest{SessionID: "target"})
			return result.Status, err
		},
		"pin": func() (operationresult.Status, error) {
			result, err := m.SetSessionPinned(testContext(false), SetSessionPinnedRequest{SessionID: "target", Pinned: true})
			return result.Status, err
		},
		"unpin": func() (operationresult.Status, error) {
			result, err := m.SetSessionPinned(testContext(false), SetSessionPinnedRequest{SessionID: "target"})
			return result.Status, err
		},
		"delete": func() (operationresult.Status, error) {
			result, err := m.DeleteSession(testContext(false), DeleteSessionRequest{SessionID: "target", Confirmed: true})
			return result.Status, err
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			provider.target, provider.failure, provider.failureStatus = "", nil, ""
			status, err := operation()
			if err != nil || status != operationresult.Success || provider.target != "target" {
				t.Fatalf("target=%q error=%v", provider.target, err)
			}
			provider.failureStatus = operationresult.Busy
			status, err = operation()
			if err != nil || status != operationresult.Busy {
				t.Fatal("provider admission error lost")
			}
		})
	}
	provider.target, provider.failure, provider.failureStatus = "", nil, ""
	pinned, err := m.SetSessionPinned(testContext(false), SetSessionPinnedRequest{Pinned: true})
	if err != nil || pinned.Status != operationresult.Success || provider.target != "current" {
		t.Fatalf("current session target=%q error=%v", provider.target, err)
	}
	provider.failureStatus = operationresult.InvalidArguments
	renamed, err := m.RenameSession(testContext(false), RenameSessionRequest{Title: ""})
	if err != nil || renamed.Status != operationresult.InvalidArguments || provider.title != "" {
		t.Fatal("control repeated provider title validation")
	}
	provider.failureStatus = ""
	provider.failure = errors.New("session store failure")
	_, err = m.RenameSession(testContext(false), RenameSessionRequest{Title: "title"})
	if !errors.Is(err, provider.failure) {
		t.Fatalf("provider error = %v", err)
	}
}

func TestRenameSessionResolvesUniqueTitleAcrossPages(t *testing.T) {
	provider := &managedSessionStub{
		sessionStub: sessionStub{current: &storage.Session{ID: "current"}},
		rows:        map[string]*storage.Session{},
		listed: []storage.SessionSummary{
			{ID: "other", Title: "known title alpha"},
			{ID: "target", Title: "known title"},
		},
	}
	m, _ := testSupervisor(t, SupervisorDeps{AgentDeps: AgentDeps{Sessions: provider, History: provider, Scope: func(context.Context) session.Scope { return session.Scope{} }}})
	renamed, err := m.RenameSession(testContext(false), RenameSessionRequest{Target: "known title", Title: "new title", PageSize: 1})
	if err != nil || renamed.Status != operationresult.Success {
		t.Fatalf("rename: %+v %v", renamed, err)
	}
	if provider.target != "target" || provider.title != "new title" || len(provider.queries) != 2 {
		t.Fatalf("target=%q title=%q queries=%+v", provider.target, provider.title, provider.queries)
	}
}

func TestRenameSessionRejectsDuplicateTitleAndFallsBackForMissingTitle(t *testing.T) {
	provider := &managedSessionStub{
		sessionStub: sessionStub{current: &storage.Session{ID: "current"}},
		rows:        map[string]*storage.Session{},
		listed: []storage.SessionSummary{
			{ID: "one", Title: "shared title"},
			{ID: "two", Title: "shared title"},
			{ID: "three", Title: "shared title"},
		},
	}
	m, _ := testSupervisor(t, SupervisorDeps{AgentDeps: AgentDeps{Sessions: provider, History: provider, Scope: func(context.Context) session.Scope { return session.Scope{} }}})
	duplicate, err := m.RenameSession(testContext(false), RenameSessionRequest{Target: "shared title", Title: "replacement", PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, duplicate, operationresult.InvalidArguments)
	if provider.target != "" || len(provider.queries) != 2 {
		t.Fatalf("ambiguous title: target=%q pages=%d", provider.target, len(provider.queries))
	}
	provider.listed = nil
	provider.queries = nil
	renamed, err := m.RenameSession(testContext(false), RenameSessionRequest{Target: "new current", Title: "title", PageSize: 1})
	if err != nil || renamed.Status != operationresult.Success {
		t.Fatalf("fallback rename: %+v %v", renamed, err)
	}
	if provider.target != "current" || provider.title != "new current title" {
		t.Fatalf("fallback target=%q title=%q", provider.target, provider.title)
	}
}

type sessionListStub struct {
	sessionStub
	request session.ListRequest
}

func (s *sessionListStub) ListPage(_ context.Context, _ session.Scope, req session.ListRequest) (operationresult.Result[session.ListResult], error) {
	s.request = req
	return operationresult.Result[session.ListResult]{Status: operationresult.Success, Data: session.ListResult{Page: req.Page, PageSize: req.PageSize, TotalPages: 8}}, nil
}

func TestSessionListForwardsOptionsWithoutCaps(t *testing.T) {
	provider := &sessionListStub{sessionStub: sessionStub{currentStatus: operationresult.NoCurrentSession}}
	m, _ := testSupervisor(t, SupervisorDeps{AgentDeps: AgentDeps{Sessions: provider, Scope: func(context.Context) session.Scope { return session.Scope{} }}})
	result, err := m.ListSessions(testContext(false), ListSessionsRequest{Query: "chosen", Page: 3, PageSize: 250, Archived: true, ExcludeCurrent: true})
	if err != nil {
		t.Fatal(err)
	}
	want := session.ListRequest{Query: "chosen", Page: 3, PageSize: 250, ArchivedOnly: true, ExcludeCurrent: true}
	if result.Status != operationresult.Success || provider.request != want || result.Data.PageSize != 250 || result.Data.Page.Page != 3 {
		t.Fatalf("request=%+v result=%+v", provider.request, result)
	}
}

func TestSessionListRequiresSuperadminForAllPlatforms(t *testing.T) {
	provider := &sessionListStub{sessionStub: sessionStub{currentStatus: operationresult.NoCurrentSession}}
	m, _ := testSupervisor(t, SupervisorDeps{AgentDeps: AgentDeps{Sessions: provider, Scope: func(context.Context) session.Scope { return session.Scope{} }}})
	forbidden, err := m.ListSessions(testContext(false), ListSessionsRequest{Page: 1, PageSize: 10, AllPlatforms: true})
	if err != nil {
		t.Fatal(err)
	}
	if forbidden.Status != operationresult.Forbidden {
		t.Fatal("non-superadmin all-platform query accepted")
	}
	result, err := m.ListSessions(testContext(true), ListSessionsRequest{Page: 1, PageSize: 10, AllPlatforms: true})
	if err != nil || result.Status != operationresult.Success || !provider.request.AllPlatforms || result.Data.Page.Page != 1 {
		t.Fatalf("request=%+v result=%+v err=%v", provider.request, result, err)
	}
}

func TestResumeAndForkRequireActor(t *testing.T) {
	m, _ := testSupervisor(t, SupervisorDeps{})
	resumed, err := m.ResumeSession(context.Background(), ResumeSessionRequest{})
	if err != nil || resumed.Status != operationresult.Forbidden {
		t.Fatalf("resume result=%+v err=%v", resumed, err)
	}
	forked, err := m.ForkSession(context.Background(), ForkSessionRequest{})
	if err != nil || forked.Status != operationresult.Forbidden {
		t.Fatalf("fork result=%+v err=%v", forked, err)
	}
}
