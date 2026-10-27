// reference_test.go 验证引用索引、别名范围、事件引用内容及发送后的别名登记。

package qqofficial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/platform/share"
	"elbot/internal/storage"
)

func mapTestReference(t *testing.T, a *Adapter, scopeID, alias, id string) {
	t.Helper()
	if err := a.chatHistory.MapPlatformMessageAlias(t.Context(), storage.PlatformMessageAlias{
		Platform: platformName, PlatformScopeID: scopeID, Alias: alias, PlatformMessageID: id,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInboundReferenceIndexes(t *testing.T) {
	for _, tc := range []struct {
		name, raw, own, ref string
	}{
		{"scene", `{"message_scene":{"ext":["invalid","other=skip","msg_idx= OWN=1 "," ref_msg_idx = REF ","ref_msg_idx="]}}`, "OWN=1", "REF"},
		{"element wins", `{"message_type":103,"message_scene":{"ext":["msg_idx=OWN","ref_msg_idx=scene"]},"msg_elements":[{"content":"quote"},{"msg_idx":"element"}]}`, "OWN", "element"},
		{"ordinary element", `{"msg_elements":[{"msg_idx":"not-a-quote","content":"other"}]}`, "", ""},
		{"empty", `{}`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var msg inboundMessage
			if err := json.Unmarshal([]byte(tc.raw), &msg); err != nil {
				t.Fatal(err)
			}
			own, ref := inboundReferenceIndexes(msg)
			if own != tc.own || ref != tc.ref {
				t.Fatalf("indexes = %q/%q", own, ref)
			}
		})
	}
}

func TestReferenceAliasKeepsScope(t *testing.T) {
	a := newTestAdapter(t, Config{CommandPrefixes: []string{"/"}}, nil)
	a.referenceResolver = func(_ context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
		if opts.ScopeID != "group:group" || opts.ReplyID != "original" {
			t.Fatalf("options = %+v", opts)
		}
		return share.ReferenceResult{Reply: share.ReplyContext{MessageID: "original", Text: "meeting at three"}, Text: "[引用:meeting at three]\n\nremember"}, nil
	}
	h := &captureHandler{}
	a.handleGroupMessage(t.Context(), h.HandleMessage, payload{Type: eventGroupMessageCreate}, inboundMessage{
		ID: "original", GroupOpenID: "group", Author: inboundAuthor{MemberOpenID: "author"}, Content: "meeting at three",
		MessageScene: messageScene{Ext: []string{"msg_idx=REFIDX_original"}},
	})
	for _, group := range []string{"group", "other"} {
		h = &captureHandler{}
		a.handleGroupMessage(t.Context(), h.HandleMessage, payload{Type: eventGroupAtMessageCreate}, inboundMessage{
			ID: "reply", GroupOpenID: group, Author: inboundAuthor{MemberOpenID: "reader"}, Content: "@bot remember",
			MessageScene: messageScene{Ext: []string{"ref_msg_idx=REFIDX_original"}},
		})
		got, ok := share.MessageContextFrom(h.ctx)
		if !ok || h.text != "remember" {
			t.Fatalf("handler context=%v text=%q", ok, h.text)
		}
		if group == "group" {
			if got.ReplyToMessageID != "original" || got.Reply.Text != "meeting at three" || !strings.Contains(got.ContextText, "meeting at three") {
				t.Fatalf("history reference=%+v", got)
			}
		} else if got.ReplyToMessageID != "" || got.ContextText != "" {
			t.Fatalf("cross-scope reference leaked: %+v", got)
		}
	}
}

func TestEventReferenceWithoutHistory(t *testing.T) {
	for _, group := range []bool{false, true} {
		for _, tc := range []struct {
			name, content, quote string
			messageType          int
			attachments          []messageAttachment
			want                 string
		}{
			{name: "text", content: "remember", quote: "meeting", messageType: 103, want: "[引用:meeting]\n\nremember"},
			{name: "image", content: "describe", messageType: 103, attachments: []messageAttachment{{URL: "https://example.com/image.png", ContentType: "image/png"}}, want: "[引用:[媒体消息]]\n\ndescribe"},
			{name: "file", content: "read", messageType: 103, attachments: []messageAttachment{{URL: "https://example.com/file.txt", Filename: "file.txt", ContentType: "text/plain"}}, want: "[引用:[媒体消息]]\n\nread"},
			{name: "command", content: "/help", quote: "meeting", messageType: 103},
			{name: "missing", content: "remember", messageType: 103},
			{name: "ordinary", content: "remember", quote: "not a quote"},
		} {
			t.Run(fmt.Sprintf("group=%v/%s", group, tc.name), func(t *testing.T) {
				a := newTestAdapter(t, Config{CommandPrefixes: []string{"/"}}, nil)
				msg := inboundMessage{ID: "current", Author: inboundAuthor{UserOpenID: "user", MemberOpenID: "user"}, GroupOpenID: "group",
					Content: tc.content, MessageType: tc.messageType,
					MsgElements: []messageElement{{MsgIdx: "REFIDX_unknown", Content: tc.quote, Attachments: tc.attachments}},
				}
				h := &captureHandler{}
				if group {
					a.handleGroupMessage(t.Context(), h.HandleMessage, payload{Type: eventGroupMessageCreate}, msg)
				} else {
					a.handleC2CMessage(t.Context(), h.HandleMessage, payload{Type: eventC2CMessageCreate}, msg)
				}
				got, ok := share.MessageContextFrom(h.ctx)
				if !ok || h.text != tc.content || got.RawText != tc.content || got.ContextText != tc.want {
					t.Fatalf("context text=%q, want=%q, raw=%q", got.ContextText, tc.want, got.RawText)
				}
				if got.ReplyToMessageID != "" || got.Reply.MessageID != "" || got.ResumeSessionID != "" || got.ForkFromMessageID != "" {
					t.Fatalf("unmapped reference acquired identity: %+v", got)
				}
				if len(tc.attachments) > 0 {
					if len(got.ContextSegments) != 2 || got.ContextSegments[1].URL != tc.attachments[0].URL || len(got.Segments) != 1 {
						t.Fatalf("reference segments=%+v current=%+v", got.ContextSegments, got.Segments)
					}
				}
			})
		}
	}
}

type failingAliasHistory struct{ interfaceHistory }

type interfaceHistory interface {
	share.ChatHistoryWriter
	MapPlatformMessageAlias(context.Context, storage.PlatformMessageAlias) error
	ResolvePlatformMessageAlias(context.Context, string, string, string) (string, error)
}

func TestMappedReferenceConsumesResolverMediaAndSuppliesEventFallback(t *testing.T) {
	for _, fromHistory := range []bool{false, true} {
		t.Run(fmt.Sprintf("history=%v", fromHistory), func(t *testing.T) {
			a := newTestAdapter(t, Config{CommandPrefixes: []string{"/"}}, nil)
			mapTestReference(t, a, "c2c:user", "REFIDX_original", "original")
			image := messageAttachment{URL: "https://example.com/quote.png", ContentType: "image/png", Filename: "quote.png"}
			if fromHistory {
				a.handleC2CMessage(t.Context(), (&captureHandler{}).HandleMessage, payload{}, inboundMessage{
					ID: "original", Author: inboundAuthor{UserOpenID: "user"}, Content: "original text", Attachments: []messageAttachment{image},
				})
			}
			for _, text := range []string{"describe", "/help"} {
				a.referenceResolver = func(ctx context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
					if opts.ReplyID != "original" || opts.ScopeID != "c2c:user" {
						t.Fatalf("options = %+v", opts)
					}
					if !fromHistory {
						ref, ok, err := opts.Fetch(ctx, opts.ReplyID)
						if err != nil || !ok || ref.Text != "original text" || len(ref.Segments) != 2 {
							t.Fatalf("event reference = %+v, %v", ref, err)
						}
					}
					if text == "/help" {
						return share.ReferenceResult{Text: text}, nil
					}
					return share.ReferenceResult{Text: "[引用:original text]\n\ndescribe", Reply: share.ReplyContext{MessageID: "original", Text: "original text"}, ReferenceSegments: []share.MessageSegment{{Type: share.SegmentImage, URL: image.URL}}}, nil
				}
				msg := inboundMessage{ID: "reply", Author: inboundAuthor{UserOpenID: "user"}, Content: text,
					MessageScene: messageScene{Ext: []string{"ref_msg_idx=REFIDX_original"}},
				}
				if !fromHistory {
					msg.MsgElements = []messageElement{{Content: "original text", Attachments: []messageAttachment{image}}}
				}
				h := &captureHandler{}
				a.handleC2CMessage(t.Context(), h.HandleMessage, payload{}, msg)
				got, ok := share.MessageContextFrom(h.ctx)
				if !ok || got.ReplyToMessageID != "original" || h.text != text || got.ResumeSessionID != "" || got.ForkFromMessageID != "" {
					t.Fatalf("reference context=%+v", got)
				}
				if text == "/help" {
					if got.ContextText != text || len(got.ContextSegments) != 1 {
						t.Fatalf("command decorated: %+v", got)
					}
					continue
				}
				if got.Reply.Text != "original text" || got.Reply.MessageID != "original" || len(got.ContextSegments) != 2 || got.ContextSegments[1].URL != image.URL {
					t.Fatalf("media reference=%+v", got)
				}
				if strings.Count(got.ContextText, "original text") != 1 {
					t.Fatalf("duplicate quote: %q", got.ContextText)
				}
			}
		})
	}
}

func (failingAliasHistory) MapPlatformMessageAlias(context.Context, storage.PlatformMessageAlias) error {
	return errors.New("alias write failed")
}

func TestSendRegistersReferenceIndexWithoutChangingDelivery(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("writeFailure=%v", fail), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/files") {
					fmt.Fprint(w, `{"file_info":"uploaded"}`)
					return
				}
				calls++
				fmt.Fprintf(w, `{"id":"sent-%d","ext_info":{"ref_idx":"REFIDX_%d"}}`, calls, calls)
			}))
			defer server.Close()
			a := newTestAdapter(t, Config{CommandPrefixes: []string{"/"}}, nil)
			a.client = newQQOfficialSendTestAdapter(t, server).client
			if fail {
				a.chatHistory = failingAliasHistory{a.chatHistory}
			}
			for _, group := range []bool{false, true} {
				target := sendTarget{Kind: targetC2C, OpenID: "user"}
				if group {
					target = sendTarget{Kind: targetGroup, OpenID: "group"}
				}
				for _, out := range []delivery.Output{delivery.Text("hello"), {Kind: delivery.KindImage, Source: delivery.Source{URL: "https://example.com/image.png"}}} {
					receipt, _, err := a.sendOutputs(t.Context(), target, []delivery.Output{out})
					if err != nil || len(receipt.PlatformMessageIDs) != 1 || receipt.PlatformMessageIDs[0] != fmt.Sprintf("sent-%d", calls) {
						t.Fatalf("receipt=%+v err=%v", receipt, err)
					}
					id, err := a.chatHistory.ResolvePlatformMessageAlias(t.Context(), platformName, qqOfficialTargetScope(target), fmt.Sprintf("REFIDX_%d", calls))
					if fail {
						if !errors.Is(err, storage.ErrNotFound) {
							t.Fatalf("failed write: %v", err)
						}
					} else if err != nil || id != receipt.PlatformMessageIDs[0] {
						t.Fatalf("alias=%q err=%v", id, err)
					}
				}
			}
			if calls != 4 {
				t.Fatalf("send count=%d", calls)
			}
		})
	}
}
