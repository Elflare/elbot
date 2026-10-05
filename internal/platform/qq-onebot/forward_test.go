package qqonebot

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"elbot/internal/platform"
	"elbot/internal/platform/refcontext"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

func TestForwardHistoryKeepsPlaceholderWithoutFetching(t *testing.T) {
	ctx := context.Background()
	history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	a := New(Config{}, nil, history.Repository(), nil)
	a.transport = newTestTransport(t, func(req request) response {
		t.Errorf("unquoted forward called %s", req.Action)
		return response{Status: "failed", Retcode: 1}
	})
	raw := json.RawMessage(`[{"type":"text","data":{"text":"前"}},{"type":"forward","data":{"id":"secret-forward-resource"}},{"type":"image","data":{"file":"outside-image"}},{"type":"text","data":{"text":"后"}}]`)
	handler := &captureHandler{}
	a.handleEvent(ctx, handler, Event{MessageType: "private", SelfID: 1000, UserID: 1, MessageID: 123, Message: raw})
	if handler.text != "前[forward]后" {
		t.Fatalf("text=%q", handler.text)
	}
	row, err := history.Repository().GetByPlatformMessage(ctx, "qqonebot", "private:1", "123")
	if err != nil {
		t.Fatal(err)
	}
	if row.Text != "前[forward]后" || strings.Contains(row.Raw+row.Segments+row.Metadata, "secret-forward-resource") {
		t.Fatalf("history=%#v", row)
	}
	segments := platform.UnmarshalChatSegments(row.Segments)
	if got := forwardSegmentsForTest(segments); got != "前[forward]{image:outside-image}后" {
		t.Fatalf("history order=%q", got)
	}
}

func TestForwardReferenceExpandsOneLayerInOrder(t *testing.T) {
	for _, kind := range []string{"private", "group"} {
		for _, withHistory := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/history=%v", kind, withHistory), func(t *testing.T) {
				ctx := context.Background()
				var repo storage.ChatHistoryRepository
				if withHistory {
					history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = history.Close() })
					repo = history.Repository()
				}
				original := Event{MessageType: kind, SelfID: 1000, UserID: 1, GroupID: 9, MessageID: 987654321, Sender: Sender{Nickname: "外层发送者"}, Message: json.RawMessage(`[{"type":"text","data":{"text":"外层前\n"}},{"type":"forward","data":{"id":"resource-secret"}},{"type":"text","data":{"text":"\n外层后"}},{"type":"image","data":{"file":"outside-image","url":"https://example.com/outside.png"}}]`)}
				var forwardCalls atomic.Int32
				transport := newTestTransport(t, func(req request) response {
					switch req.Action {
					case "get_msg":
						if req.Params["message_id"] != float64(original.MessageID) {
							t.Errorf("outer message id=%#v", req.Params)
						}
						data, _ := json.Marshal(getMessageData{MessageID: original.MessageID, UserID: original.UserID, Sender: original.Sender, Message: original.Message})
						return response{Data: data}
					case "get_forward_msg":
						forwardCalls.Add(1)
						if req.Params["message_id"] != "resource-secret" {
							t.Errorf("unexpected/nested resource: %#v", req.Params)
						}
						return response{Data: []byte(`{"messages":[{"sender":{"user_id":555001,"nickname":"小明"},"content":[{"type":"text","data":{"text":"文字 A\n"}},{"type":"image","data":{"file":"inside-a","url":"https://example.com/a.png"}},{"type":"text","data":{"text":"\n图片之后"}},{"type":"forward","data":{"id":"nested-secret"}},{"type":"record","data":{"file":"voice"}},{"type":"video","data":{"file":"video"}},{"type":"file","data":{"file_id":"file"}},{"type":"face","data":{"id":"1"}}]},{"sender":{"user_id":555002,"nickname":"小红"},"content":[{"type":"image","data":{"file":"inside-b","url":"https://example.com/b.png"}},{"type":"text","data":{"text":"尾部文字"}}]}]}`)}
					default:
						t.Errorf("unexpected API %s", req.Action)
						return response{Status: "failed", Retcode: 1}
					}
				})
				a := New(Config{}, nil, repo, nil)
				a.transport = transport
				if withHistory {
					a.recordChatMessage(ctx, original, normalizeMessage(original.Message, "", original.SelfID), platform.ReplyContext{})
				}
				handler := &captureHandler{}
				a.handleEvent(ctx, handler, Event{MessageType: kind, SelfID: 1000, UserID: 1, GroupID: 9, MessageID: 222, Message: json.RawMessage(`[{"type":"reply","data":{"id":"987654321"}},{"type":"text","data":{"text":"问题前"}},{"type":"image","data":{"file":"current-image"}},{"type":"text","data":{"text":"问题后"}}]`)})
				msg, ok := platform.MessageContextFrom(handler.ctx)
				if !ok {
					t.Fatal("missing message context")
				}
				want := "外层前\n<forward_message>\n小明：文字 A\n{image:inside-a}\n图片之后[forward][语音][视频][文件][表情]\n\n小红：{image:inside-b}尾部文字\n</forward_message>\n外层后{image:outside-image}\n\n问题前{image:current-image}问题后"
				if got := forwardSegmentsForTest(msg.ContextSegments); got != want {
					t.Fatalf("display=%q, want %q", got, want)
				}
				if forwardCalls.Load() != 1 {
					t.Fatalf("forward calls=%d", forwardCalls.Load())
				}
				if msg.Reply.MessageID != "987654321" {
					t.Fatalf("internal reply id=%q", msg.Reply.MessageID)
				}
				for _, secret := range []string{"987654321", "resource-secret", "nested-secret", "555001", "555002"} {
					if strings.Contains(msg.ContextText+forwardSegmentsForTest(msg.ContextSegments), secret) {
						t.Fatalf("display leaked %q", secret)
					}
				}
				if got := forwardSegmentsForTest(msg.Reply.Segments); got != "外层前\n[forward]\n外层后{image:outside-image}" {
					t.Fatalf("original reference positions changed: %q", got)
				}
				if repo != nil {
					row, err := repo.GetByPlatformMessage(ctx, "qqonebot", scopeID(original), "987654321")
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(row.Text+row.Segments, "inside-a") || !strings.Contains(row.Text, "[forward]") {
						t.Fatalf("history expanded in place: %#v", row)
					}
				}
			})
		}
	}
}

func TestForwardReferenceFallbacks(t *testing.T) {
	for _, failure := range []string{"get_msg", "get_forward_msg", "empty", "malformed", "missing resource"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			a := New(Config{}, nil, nil, nil)
			a.transport = newTestTransport(t, func(req request) response {
				calls.Add(1)
				if req.Action == failure {
					return response{Status: "failed", Retcode: 1}
				}
				if req.Action == "get_msg" {
					if failure == "missing resource" {
						return response{Data: []byte(`{"message":[{"type":"text","data":{"text":"前"}},{"type":"forward","data":{}},{"type":"text","data":{"text":"后"}}]}`)}
					}
					return response{Data: []byte(`{"message":[{"type":"text","data":{"text":"前"}},{"type":"forward","data":{"id":"secret-resource"}},{"type":"text","data":{"text":"后"}}]}`)}
				}
				if failure == "malformed" {
					return response{Data: []byte(`{"messages":"invalid"}`)}
				}
				return response{Data: []byte(`{"messages":[]}`)}
			})
			ref := refcontext.ReferencedMessage{Text: "前[forward]后", Segments: []platform.MessageSegment{textSegment("前"), textSegment("[forward]"), textSegment("后")}}
			got := forwardSegmentsForTest(a.expandForwardReference(Event{})(context.Background(), "987654321", ref))
			if !strings.Contains(got, "[forward]") || !strings.HasPrefix(got, "前") || !strings.HasSuffix(got, "后") || strings.Contains(got, "987654321") || strings.Contains(got, "secret-resource") {
				t.Fatalf("fallback=%q", got)
			}
			wantCalls := int32(2)
			if failure == "get_msg" || failure == "missing resource" {
				wantCalls = 1
			}
			if calls.Load() != wantCalls {
				t.Fatalf("calls=%d want=%d", calls.Load(), wantCalls)
			}
		})
	}
}

func TestMultipleForwardsKeepOrderAndPartialContent(t *testing.T) {
	a := New(Config{}, nil, nil, nil)
	var calls atomic.Int32
	a.transport = newTestTransport(t, func(req request) response {
		if req.Action == "get_msg" {
			return response{Data: []byte(`{"message":[{"type":"forward","data":{"id":"first"}},{"type":"text","data":{"text":"中间"}},{"type":"forward","data":{"id":"second"}}]}`)}
		}
		calls.Add(1)
		if req.Params["message_id"] == "first" {
			return response{Data: []byte(`{"messages":[{"content":[{"type":"text","data":{"text":"成功的正文"}},{"type":"forward","data":{"id":"nested"}}]}]}`)}
		}
		return response{Status: "failed", Retcode: 1}
	})
	got := a.expandForwardReference(Event{})(context.Background(), "123", refcontext.ReferencedMessage{Text: "[forward]中间[forward]"})
	want := "<forward_message>\n成功的正文[forward]\n</forward_message>中间<forward_message>\n[forward]\n</forward_message>"
	if text := forwardSegmentsForTest(got); text != want || calls.Load() != 2 {
		t.Fatalf("display=%q calls=%d", text, calls.Load())
	}
}

func TestCanonicalForwardReferenceDoesNotFetchAgain(t *testing.T) {
	a := New(Config{}, nil, nil, nil)
	a.transport = newTestTransport(t, func(req request) response {
		t.Errorf("canonical forward queried %s", req.Action)
		return response{Status: "failed", Retcode: 1}
	})
	ref := refcontext.ReferencedMessage{Text: "完整 assistant 正文", Segments: []platform.MessageSegment{textSegment("[forward]")}, CanonicalContent: true}
	got := a.expandForwardReference(Event{})(context.Background(), "123", ref)
	if text := forwardSegmentsForTest(got); text != "<forward_message>\n完整 assistant 正文\n</forward_message>" {
		t.Fatal(text)
	}
}

func forwardSegmentsForTest(segments []platform.MessageSegment) string {
	var out strings.Builder
	for _, segment := range segments {
		switch segment.Type {
		case platform.SegmentText:
			out.WriteString(segment.Text)
		case platform.SegmentImage:
			out.WriteString("{image:" + segment.PlatformFileID + "}")
		case platform.SegmentFile:
			out.WriteString("{file:" + segment.PlatformFileID + "}")
		default:
			out.WriteString("{" + string(segment.Type) + "}")
		}
	}
	return out.String()
}
