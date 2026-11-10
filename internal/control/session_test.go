package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type historyStub struct {
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
	message := storage.Message{ID: "m", SessionID: "s", Role: "assistant", Content: "exact\n", SourceContent: "model source", ReasoningContent: "thought", ParentMessageID: "parent", ReplyToPlatformMessageID: "platform-message", ReplyToMessageID: "reply", ToolCallID: "tool-call", ToolCalls: []storage.ToolCall{{ID: "call-1", Name: "shell", Arguments: `{"command":"pwd"}`}}, ToolBatchStatus: "completed", Metadata: `{"internal":"must not be returned"}`, Segments: string(raw), CreatedAt: storage.Now()}
	provider.messages = []storage.Message{message}
	m, _ := testSupervisor(t, SupervisorDeps{History: provider, Scope: func(context.Context) session.Scope {
		return session.Scope{Platform: "qq", ChatType: contextinfo.ChatPrivate, ChatID: "chat", Owner: contextinfo.ActorIdentity{Platform: "qq", ID: "user"}}
	}})
	_, err = m.SessionMessages(testContext(false), SessionMessagesRequest{SessionID: "s", Page: 1, PageSize: 250})
	requireCode(t, err, "permission_denied")
	if provider.reads != 0 {
		t.Fatal("foreign session history loaded before access check")
	}
	provider.row.Owner.ID = "user"
	result, err := m.SessionMessages(testContext(false), SessionMessagesRequest{SessionID: "s", Page: 1, PageSize: 250})
	if err != nil {
		t.Fatal(err)
	}
	want := MessageData{ID: message.ID, SessionID: message.SessionID, Role: message.Role, Content: message.Content, SourceContent: message.SourceContent, ReasoningContent: message.ReasoningContent, ParentMessageID: message.ParentMessageID, ReplyToPlatformMessageID: message.ReplyToPlatformMessageID, ReplyToMessageID: message.ReplyToMessageID, ToolCallID: message.ToolCallID, Segments: segments, ToolCalls: []ToolCallData{{ID: "call-1", Name: "shell", Arguments: `{"command":"pwd"}`}}, ToolBatchStatus: message.ToolBatchStatus, CreatedAt: message.CreatedAt}
	if !reflect.DeepEqual(result.Items, []MessageData{want}) {
		t.Fatalf("history data: %+v", result)
	}
	encoded, err := json.Marshal(result.Items[0])
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
	if provider.page != 1 || provider.pageSize != 250 || result.PageSize != 250 {
		t.Fatal("caller-selected pagination was changed")
	}
	_, err = m.SessionMessages(testContext(false), SessionMessagesRequest{SessionID: "s", Page: 2, PageSize: 250})
	requireCode(t, err, "invalid_arguments")
	_, err = m.SessionMessages(testContext(false), SessionMessagesRequest{SessionID: "s", Page: 1})
	requireCode(t, err, "invalid_arguments")
}

func TestMessageDataReturnsToolNameFromStructuredField(t *testing.T) {
	rows := []storage.Message{{ID: "tool-result", Role: storage.RoleTool, ToolCallID: "call-1", ToolName: "shell"}}
	items, err := messageData(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ToolName != "shell" || items[0].ToolCallID != "call-1" {
		t.Fatalf("message data = %+v", items)
	}
}

type managedSessionStub struct {
	sessionStub
	target  string
	title   string
	failure error
	rows    map[string]*storage.Session
	listed  []storage.SessionSummary
	queries []session.ListRequest
}

func (*managedSessionStub) CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error) {
	panic("management operation inspected the current binding")
}
func (s *managedSessionStub) Rename(_ context.Context, _ session.Scope, id, title string) (*storage.Session, error) {
	s.target, s.title = id, title
	return &storage.Session{ID: id}, s.failure
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
func (s *managedSessionStub) ListPage(_ context.Context, _ session.Scope, req session.ListRequest) (session.ListResult, error) {
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
	return session.ListResult{Items: items, Page: req.Page, PageSize: req.PageSize, TotalPages: pages}, nil
}
func (s *managedSessionStub) Archive(ctx context.Context, scope session.Scope, id string) (*storage.Session, error) {
	return s.Rename(ctx, scope, id, "")
}
func (s *managedSessionStub) Unarchive(ctx context.Context, scope session.Scope, id string) (*storage.Session, error) {
	return s.Rename(ctx, scope, id, "")
}
func (s *managedSessionStub) Pin(ctx context.Context, scope session.Scope, id string) (*storage.Session, error) {
	return s.Rename(ctx, scope, id, "")
}
func (s *managedSessionStub) Unpin(ctx context.Context, scope session.Scope, id string) (*storage.Session, error) {
	return s.Rename(ctx, scope, id, "")
}
func (s *managedSessionStub) Delete(_ context.Context, _ session.Scope, id string) error {
	s.target = id
	return s.failure
}

func TestSessionManagementUsesExplicitTargetAndProviderAdmission(t *testing.T) {
	provider := &managedSessionStub{sessionStub: sessionStub{current: &storage.Session{ID: "current"}}, rows: map[string]*storage.Session{"target": {ID: "target"}}}
	m, _ := testSupervisor(t, SupervisorDeps{Sessions: provider, History: provider, Scope: func(context.Context) session.Scope { return session.Scope{} }})
	operations := map[string]func() error{
		"rename": func() error {
			_, err := m.RenameSession(testContext(false), RenameSessionRequest{Target: "target", Title: "title", PageSize: 10})
			return err
		},
		"archive": func() error {
			_, err := m.SetSessionArchived(testContext(false), SetSessionArchivedRequest{SessionID: "target", Archived: true, Confirmed: true})
			return err
		},
		"unarchive": func() error {
			_, err := m.SetSessionArchived(testContext(false), SetSessionArchivedRequest{SessionID: "target"})
			return err
		},
		"pin": func() error {
			_, err := m.SetSessionPinned(testContext(false), SetSessionPinnedRequest{SessionID: "target", Pinned: true})
			return err
		},
		"unpin": func() error {
			_, err := m.SetSessionPinned(testContext(false), SetSessionPinnedRequest{SessionID: "target"})
			return err
		},
		"delete": func() error {
			_, err := m.DeleteSession(testContext(false), DeleteSessionRequest{SessionID: "target", Confirmed: true})
			return err
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			provider.target, provider.failure = "", nil
			if err := operation(); err != nil || provider.target != "target" {
				t.Fatalf("target=%q error=%v", provider.target, err)
			}
			provider.failure = session.ErrSessionBusy
			err := operation()
			requireCode(t, err, "session_busy")
			if !errors.Is(err, session.ErrSessionBusy) {
				t.Fatal("provider admission error lost")
			}
		})
	}
	provider.target, provider.failure = "", nil
	if _, err := m.SetSessionPinned(testContext(false), SetSessionPinnedRequest{Pinned: true}); err != nil || provider.target != "current" {
		t.Fatalf("current session target=%q error=%v", provider.target, err)
	}
	provider.failure = errors.New("provider title validation")
	_, err := m.RenameSession(testContext(false), RenameSessionRequest{Title: ""})
	if !errors.Is(err, provider.failure) || provider.title != "" {
		t.Fatal("control repeated provider title validation")
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
	m, _ := testSupervisor(t, SupervisorDeps{Sessions: provider, History: provider, Scope: func(context.Context) session.Scope { return session.Scope{} }})
	if _, err := m.RenameSession(testContext(false), RenameSessionRequest{Target: "known title", Title: "new title", PageSize: 1}); err != nil {
		t.Fatal(err)
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
		},
	}
	m, _ := testSupervisor(t, SupervisorDeps{Sessions: provider, History: provider, Scope: func(context.Context) session.Scope { return session.Scope{} }})
	_, err := m.RenameSession(testContext(false), RenameSessionRequest{Target: "shared title", Title: "replacement", PageSize: 1})
	requireCode(t, err, "invalid_arguments")
	if provider.target != "" {
		t.Fatalf("duplicate title reached provider: %+v", provider)
	}
	provider.listed = nil
	provider.queries = nil
	if _, err := m.RenameSession(testContext(false), RenameSessionRequest{Target: "new current", Title: "title", PageSize: 1}); err != nil {
		t.Fatal(err)
	}
	if provider.target != "current" || provider.title != "new current title" {
		t.Fatalf("fallback target=%q title=%q", provider.target, provider.title)
	}
}

type sessionListStub struct {
	sessionStub
	request session.ListRequest
}

func (s *sessionListStub) ListPage(_ context.Context, _ session.Scope, req session.ListRequest) (session.ListResult, error) {
	s.request = req
	return session.ListResult{Page: req.Page, PageSize: req.PageSize, TotalPages: 8}, nil
}

func TestSessionListForwardsOptionsWithoutCaps(t *testing.T) {
	provider := &sessionListStub{sessionStub: sessionStub{currentErr: session.ErrNoCurrentSession}}
	m, _ := testSupervisor(t, SupervisorDeps{Sessions: provider, Scope: func(context.Context) session.Scope { return session.Scope{} }})
	result, err := m.ListSessions(testContext(false), ListSessionsRequest{Query: "chosen", Page: 3, PageSize: 250, Archived: true, ExcludeCurrent: true})
	if err != nil {
		t.Fatal(err)
	}
	want := session.ListRequest{Query: "chosen", Page: 3, PageSize: 250, ArchivedOnly: true, ExcludeCurrent: true}
	if provider.request != want || result.PageSize != 250 || result.Page.Page != 3 {
		t.Fatalf("request=%+v result=%+v", provider.request, result)
	}
}
