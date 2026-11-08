package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
	message := storage.Message{ID: "m", SessionID: "s", Role: "assistant", Content: "exact\n", ParentMessageID: "parent", ReplyToPlatformMessageID: "platform-message", ReplyToMessageID: "reply", ToolCallID: "tool-call", Metadata: `{"source":"test"}`, Segments: string(raw), CreatedAt: storage.Now()}
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
	want := MessageData{ID: message.ID, SessionID: message.SessionID, Role: message.Role, Content: message.Content, ParentMessageID: message.ParentMessageID, ReplyToPlatformMessageID: message.ReplyToPlatformMessageID, ReplyToMessageID: message.ReplyToMessageID, ToolCallID: message.ToolCallID, Metadata: message.Metadata, Segments: segments, CreatedAt: message.CreatedAt}
	if !reflect.DeepEqual(result.Items, []MessageData{want}) {
		t.Fatalf("history data: %+v", result)
	}
	if provider.page != 1 || provider.pageSize != 250 || result.PageSize != 250 {
		t.Fatal("caller-selected pagination was changed")
	}
	_, err = m.SessionMessages(testContext(false), SessionMessagesRequest{SessionID: "s", Page: 2, PageSize: 250})
	requireCode(t, err, "invalid_arguments")
	_, err = m.SessionMessages(testContext(false), SessionMessagesRequest{SessionID: "s", Page: 1})
	requireCode(t, err, "invalid_arguments")
}

type managedSessionStub struct {
	sessionStub
	target  string
	title   string
	failure error
}

func (*managedSessionStub) CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error) {
	panic("management operation inspected the current binding")
}
func (s *managedSessionStub) Rename(_ context.Context, _ session.Scope, id, title string) (*storage.Session, error) {
	s.target, s.title = id, title
	return &storage.Session{ID: id}, s.failure
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
	provider := &managedSessionStub{sessionStub: sessionStub{current: &storage.Session{ID: "current"}}}
	m, _ := testSupervisor(t, SupervisorDeps{Sessions: provider, Scope: func(context.Context) session.Scope { return session.Scope{} }})
	operations := map[string]func() error{
		"rename": func() error {
			_, err := m.RenameSession(testContext(false), RenameSessionRequest{SessionID: "target", Title: "title"})
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
	provider.failure = errors.New("provider title validation")
	_, err := m.RenameSession(testContext(false), RenameSessionRequest{SessionID: "target"})
	if !errors.Is(err, provider.failure) || provider.title != "" {
		t.Fatal("control repeated provider title validation")
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
