package qqonebot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/platform"
	"elbot/internal/platform/refcontext"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

// The live service returns message objects, with the body in message, not content.
const twoTextForwardResponse = `{"messages":[
	{"message_id":101,"sender":{"user_id":1,"nickname":"Elflare"},"message_format":"array","message":[{"type":"text","data":{"text":"第一条文本\n保留换行"}}]},
	{"message_id":102,"sender":{"user_id":1,"nickname":"Elflare"},"message_format":"array","message":[{"type":"text","data":{"text":"第二条文本"}}]}
]}`

const twoTextForwardDisplay = "<forward_message>\nElflare：第一条文本\n保留换行\n\nElflare：第二条文本\n</forward_message>"

func TestForwardReferenceReadsMessageNodeBodies(t *testing.T) {
	a := New(Config{}, nil, nil)
	a.transport = newTestTransport(t, func(req request) response {
		switch req.Action {
		case "get_msg":
			return response{Data: []byte(`{"message":[{"type":"forward","data":{"id":"resource"}}]}`)}
		case "get_forward_msg":
			return response{Data: []byte(twoTextForwardResponse)}
		default:
			t.Errorf("unexpected API %s", req.Action)
			return response{Status: "failed", Retcode: 1}
		}
	})
	got := a.expandForwardReference(Event{})(context.Background(), "123", refcontext.ReferencedMessage{Text: "[forward]"})
	if text := forwardSegmentsForTest(got); text != twoTextForwardDisplay {
		t.Fatalf("display=%q, want %q", text, twoTextForwardDisplay)
	}
}

func TestPrivateForwardExpandsWithoutReference(t *testing.T) {
	ctx := context.Background()
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	a := New(Config{}, nil, history.Repository())
	calls := 0
	a.transport = newTestTransport(t, func(req request) response {
		calls++
		if req.Action != "get_forward_msg" || req.Params["message_id"] != "resource" {
			t.Errorf("unexpected API: %#v", req)
			return response{Status: "failed", Retcode: 1}
		}
		return response{Data: []byte(twoTextForwardResponse)}
	})
	handler := &captureHandler{}
	a.handleEvent(ctx, handler, Event{MessageType: "private", SelfID: 1000, UserID: 1, MessageID: 123, Message: json.RawMessage(`[{"type":"forward","data":{"id":"resource"}}]`)})
	msg, ok := platform.MessageContextFrom(handler.ctx)
	if !ok || handler.count != 1 {
		t.Fatal("missing message context")
	}
	if got := forwardSegmentsForTest(msg.ContextSegments); got != twoTextForwardDisplay || msg.ContextText != twoTextForwardDisplay {
		t.Fatalf("display=%q text=%q", got, msg.ContextText)
	}
	if calls != 1 || handler.text != "[forward]" || msg.RawText != "[forward]" || msg.Reply.MessageID != "" {
		t.Fatalf("calls=%d input=%q raw=%q reply=%#v", calls, handler.text, msg.RawText, msg.Reply)
	}
	if got := forwardSegmentsForTest(msg.Segments); got != "[forward]" {
		t.Fatalf("source segments=%q", got)
	}
	row, err := history.Repository().GetByPlatformMessage(ctx, "qqonebot", "private:1", "123")
	if err != nil || row.Text != "[forward]" || strings.Contains(row.Segments, "第一条") {
		t.Fatalf("history=%#v err=%v", row, err)
	}
}

func TestForwardNodeBodyFields(t *testing.T) {
	for _, tc := range []struct{ name, node, want string }{
		{"message", `{"message":[{"type":"text","data":{"text":"正文"}}]}`, "正文"},
		{"content", `{"content":[{"type":"text","data":{"text":"兼容正文"}}]}`, "兼容正文"},
		{"message wins", `{"message":[{"type":"text","data":{"text":"实际正文"}}],"content":[{"type":"text","data":{"text":"另一份"}}]}`, "实际正文"},
		{"null message", `{"message":null,"content":[{"type":"text","data":{"text":"备用正文"}}]}`, "备用正文"},
		{"empty message stays empty", `{"message":[],"content":[{"type":"text","data":{"text":"不能拼入"}}]}`, "[forward]"},
		{"plain message", `{"message":"原始\n正文"}`, "原始\n正文"},
		{"missing body", `{}`, "[forward]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := newTestTransport(t, func(req request) response {
				return response{Data: []byte(`{"messages":[` + tc.node + `]}`)}
			})
			nodes, err := transport.GetForwardMessage(context.Background(), "resource")
			if err != nil {
				t.Fatal(err)
			}
			if got := forwardSegmentsForTest(normalizeForwardNodes(nodes)); got != tc.want {
				t.Fatalf("display=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestPrivateForwardMixedContentAndReferences(t *testing.T) {
	for _, reference := range []string{"none", "ordinary", "forward"} {
		t.Run(reference, func(t *testing.T) {
			a := New(Config{}, nil, nil)
			calls := map[string]int{}
			a.transport = newTestTransport(t, func(req request) response {
				if req.Action == "get_msg" && reference != "none" {
					body := `[{"type":"text","data":{"text":"引用正文"}},{"type":"image","data":{"file":"quoted-image"}}]`
					if reference == "forward" {
						body = `[{"type":"forward","data":{"id":"quoted"}}]`
					}
					return response{Data: []byte(`{"message_id":42,"user_id":2,"sender":{"user_id":2,"nickname":"引用者"},"message":` + body + `}`)}
				}
				if req.Action != "get_forward_msg" {
					t.Errorf("unexpected API %s", req.Action)
					return response{Status: "failed", Retcode: 1}
				}
				resource, _ := req.Params["message_id"].(string)
				calls[resource]++
				switch resource {
				case "current":
					return response{Data: []byte(`{"messages":[{"sender":{"nickname":"节点"},"message":[{"type":"text","data":{"text":"节点前\n"}},{"type":"image","data":{"file":"inside"}},{"type":"text","data":{"text":"节点后"}},{"type":"forward","data":{"id":"nested"}},{"type":"record","data":{"file":"voice"}}]}]}`)}
				case "failed":
					return response{Status: "failed", Retcode: 1}
				case "quoted":
					return response{Data: []byte(`{"messages":[{"message":[{"type":"text","data":{"text":"引用正文"}}]}]}`)}
				default:
					t.Errorf("unexpected/nested resource %q", resource)
					return response{Status: "failed", Retcode: 1}
				}
			})
			raw := `[{"type":"text","data":{"text":"外层前\n"}},{"type":"forward","data":{"id":"current"}},{"type":"text","data":{"text":"中间"}},{"type":"forward","data":{"id":"failed"}},{"type":"image","data":{"file":"outside"}},{"type":"text","data":{"text":"外层后"}}]`
			if reference != "none" {
				raw = `[{"type":"reply","data":{"id":"42"}},` + raw[1:]
			}
			handler := &captureHandler{}
			a.handleEvent(context.Background(), handler, Event{MessageType: "private", SelfID: 1000, UserID: 1, MessageID: 123, Message: json.RawMessage(raw)})
			msg, _ := platform.MessageContextFrom(handler.ctx)
			want := "外层前\n<forward_message>\n节点：节点前\n{image:inside}节点后[forward][语音]\n</forward_message>中间<forward_message>\n[forward]\n</forward_message>{image:outside}外层后"
			switch reference {
			case "ordinary":
				want = "[引用#42：引用者(qq:2):引用正文]{image:quoted-image}\n\n" + want
			case "forward":
				want = "<forward_message>\n引用正文\n</forward_message>\n\n" + want
			}
			if got := forwardSegmentsForTest(msg.ContextSegments); got != want {
				t.Fatalf("display=%q want=%q", got, want)
			}
			if calls["current"] != 1 || calls["failed"] != 1 || calls["nested"] != 0 {
				t.Fatalf("forward calls=%v", calls)
			}
			if reference == "forward" && calls["quoted"] != 1 {
				t.Fatalf("quoted calls=%v", calls)
			}
			if strings.Contains(forwardSegmentsForTest(msg.Segments), "inside") || strings.Contains(forwardSegmentsForTest(msg.Reply.Segments), "inside") {
				t.Fatal("expanded images leaked into source media indexes")
			}
		})
	}
}

func TestPrivateForwardKeepsAssistantResumeAndFork(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := &storage.Session{OwnerID: "qqonebot:1", Platform: "qqonebot", PlatformScopeID: "private:1", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	first := &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "旧回答", CreatedAt: storage.Now()}
	latest := &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "最新回答", CreatedAt: first.CreatedAt.Add(time.Second)}
	for i, message := range []*storage.Message{first, latest} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatal(err)
		}
		id := []string{"41", "42"}[i]
		if err := store.Messages().MapPlatformMessage(ctx, storage.PlatformMessageMap{Platform: "qqonebot", PlatformScopeID: "private:1", PlatformMessageID: id, MessageID: message.ID, SessionID: session.ID}); err != nil {
			t.Fatal(err)
		}
	}
	a := New(Config{}, store, nil)
	a.transport = newTestTransport(t, func(req request) response {
		if req.Action == "get_forward_msg" {
			return response{Data: []byte(twoTextForwardResponse)}
		}
		return response{Data: []byte(`{"sender":{"user_id":1000},"message":[{"type":"text","data":{"text":"回答"}}]}`)}
	})
	for _, id := range []string{"41", "42"} {
		handler := &captureHandler{}
		a.handleEvent(ctx, handler, Event{MessageType: "private", UserID: 1, SelfID: 1000, Message: json.RawMessage(`[{"type":"reply","data":{"id":"` + id + `"}},{"type":"forward","data":{"id":"current"}}]`)})
		msg, _ := platform.MessageContextFrom(handler.ctx)
		if msg.ContextText != twoTextForwardDisplay {
			t.Fatalf("assistant content duplicated or input missing: %q", msg.ContextText)
		}
		if id == "41" && (msg.ForkFromMessageID != first.ID || msg.ResumeSessionID != "") {
			t.Fatalf("fork=%q resume=%q", msg.ForkFromMessageID, msg.ResumeSessionID)
		}
		if id == "42" && (msg.ForkFromMessageID != "" || msg.ResumeSessionID != session.ID) {
			t.Fatalf("fork=%q resume=%q", msg.ForkFromMessageID, msg.ResumeSessionID)
		}
	}
}

func TestPrivateForwardTextAndCommandsDoNotFetch(t *testing.T) {
	for _, raw := range []string{
		`[{"type":"text","data":{"text":"[forward]"}}]`,
		`[{"type":"text","data":{"text":"/new "}},{"type":"forward","data":{"id":"resource"}}]`,
	} {
		a := New(Config{}, nil, nil)
		a.transport = newTestTransport(t, func(req request) response {
			t.Errorf("unexpected API %s", req.Action)
			return response{Status: "failed", Retcode: 1}
		})
		handler := &captureHandler{}
		a.handleEvent(context.Background(), handler, Event{MessageType: "private", UserID: 1, Message: json.RawMessage(raw)})
		msg, _ := platform.MessageContextFrom(handler.ctx)
		if len(msg.ContextSegments) > 0 {
			t.Fatalf("unexpected expanded content: %#v", msg.ContextSegments)
		}
	}
}
