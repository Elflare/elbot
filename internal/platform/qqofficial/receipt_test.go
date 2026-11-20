package qqofficial

import (
	"context"
	"elbot/internal/storage"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"elbot/internal/delivery"
)

type captureGroupHistory struct {
	interfaceHistory
	rows []*storage.ChatMessage
}

func (h *captureGroupHistory) Append(_ context.Context, row *storage.ChatMessage) error {
	h.rows = append(h.rows, row)
	return nil
}
func TestLocalMarkdownMaterial(t *testing.T) {
	for _, mode := range []string{"path", "data", "upload failure"} {
		t.Run(mode, func(t *testing.T) {
			var calls []string
			png := []byte("\x89PNG\r\n\x1a\nbody")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				switch r.URL.Path {
				case "/v2/media-images/prepare":
					fmt.Fprintf(w, `{"presigned_url":"http://%s/put","upload_id":"task"}`, r.Host)
				case "/put":
					b, _ := io.ReadAll(r.Body)
					if string(b) != string(png) || r.Header.Get("Content-Type") != "image/png" || r.Header.Get("Authorization") != "" {
						t.Error("incorrect PUT")
					}
					if mode == "upload failure" {
						w.WriteHeader(403)
						return
					}
					w.Header().Set("ETag", `"etag"`)
				case "/v2/media-images/complete":
					fmt.Fprint(w, `{"image_id":"42","url":"qqbot-media://image/v1/ref"}`)
				case "/v2/users/u/messages":
					var msg messageToCreate
					_ = json.NewDecoder(r.Body).Decode(&msg)
					if msg.Markdown == nil || !strings.Contains(msg.Markdown.Content, "qqbot-media://image/v1/ref") {
						t.Error("missing uploaded image")
					}
					fmt.Fprint(w, `{"id":"sent"}`)
				case "/v2/media-images/42/delete":
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			a := newQQOfficialSendTestAdapter(t, srv)
			src := delivery.Source{Data: png}
			if mode == "path" {
				p := filepath.Join(t.TempDir(), "archive")
				if err := os.WriteFile(p, png, 0600); err != nil {
					t.Fatal(err)
				}
				src = delivery.Source{Path: p}
			}
			_, err := a.SendTo(t.Context(), delivery.Target{ChatType: "private", ChatID: "u"}, []delivery.Output{delivery.Text("photo"), {Kind: delivery.KindImage, Source: src}})
			want := []string{"POST /v2/media-images/prepare", "PUT /put"}
			if mode != "upload failure" {
				want = append(want, "POST /v2/media-images/complete", "POST /v2/users/u/messages", "DELETE /v2/media-images/42/delete")
			}
			if (err != nil) != (mode == "upload failure") || !slices.Equal(calls, want) {
				t.Fatalf("calls=%v err=%v", calls, err)
			}
		})
	}
}

func TestNativeFileAndRecordOrRejectGroup(t *testing.T) {
	for _, tc := range []struct {
		k   delivery.Kind
		typ int
	}{{delivery.KindFile, 4}, {delivery.KindRecord, 3}} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if strings.HasSuffix(r.URL.Path, "/files") {
				var req uploadFileRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.FileType != tc.typ || req.SrvSendMsg {
					t.Error(req)
				}
				fmt.Fprint(w, `{"file_info":"ref"}`)
				return
			}
			fmt.Fprint(w, `{"id":"sent"}`)
		}))
		t.Cleanup(srv.Close)
		a := newQQOfficialSendTestAdapter(t, srv)
		out := delivery.Output{Kind: tc.k, Source: delivery.Source{Data: []byte("media")}}
		target := delivery.Target{ChatType: "private", ChatID: "u"}
		if _, err := a.SendTo(t.Context(), target, []delivery.Output{delivery.Text("caption"), out}); err == nil || calls != 0 {
			t.Fatalf("sent unsupported combination: %v %d", err, calls)
		}
		r, err := a.SendTo(t.Context(), target, []delivery.Output{out})
		if err != nil || calls != 2 || len(r.SentMessages) != 1 || !slices.Equal(r.SentMessages[0].OutputIndexes, []int{0}) {
			t.Fatalf("native %s: %+v %v", tc.k, r, err)
		}
	}
}
func TestNumberedPages(t *testing.T) {
	for _, n := range []int{1800, 1801, 4001} {
		for _, command := range []bool{false, true} {
			var messages []messageToCreate
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var m messageToCreate
				if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
					t.Error(err)
				}
				messages = append(messages, m)
				fmt.Fprintf(w, `{"id":"p%d"}`, len(messages))
			}))
			t.Cleanup(srv.Close)
			a := newQQOfficialSendTestAdapter(t, srv)
			md := true
			a.cfg.MarkdownByDefault = &md
			text := " " + strings.Repeat("字", n-2) + "\n"
			receipt, err := a.sendOutputs(t.Context(), sendTarget{MsgID: "incoming", Kind: targetC2C, OpenID: "u", commandReply: command}, []delivery.Output{delivery.Text(text)})
			total := (n + 1799) / 1800
			if err != nil || len(messages) != total || len(receipt.SentMessages) != total {
				t.Fatalf("n=%d pages=%d err=%v", n, len(messages), err)
			}
			var joined string
			for i, m := range messages {
				body := m.Content
				if !command {
					if m.Markdown == nil {
						t.Fatal("missing markdown")
					}
					body = m.Markdown.Content
				}
				if total > 1 {
					suffix := fmt.Sprintf("\n（%d/%d）", i+1, total)
					if !strings.HasSuffix(body, suffix) {
						t.Fatal("no page number")
					}
					body = strings.TrimSuffix(body, suffix)
				}
				if len([]rune(body)) > 1800 || m.MsgSeq != i+1 {
					t.Fatal("bad page", i)
				}
				joined += body
			}
			if joined != text {
				t.Fatal("page boundaries changed text")
			}
		}
	}
}
func TestPagedMarkdownImageReceiptAndPartialFailure(t *testing.T) {
	for _, failSecond := range []bool{false, true} {
		var msgs []messageToCreate
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var m messageToCreate
			if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
				t.Error(err)
			}
			msgs = append(msgs, m)
			if len(msgs) == 2 && failSecond {
				http.Error(w, "failed", 400)
				return
			}
			fmt.Fprintf(w, `{"id":"p%d"}`, len(msgs))
		}))
		t.Cleanup(srv.Close)
		a := newQQOfficialSendTestAdapter(t, srv)
		h := &captureGroupHistory{interfaceHistory: a.chatHistory}
		a.chatHistory = h
		group := []delivery.Output{delivery.Text(strings.Repeat("字", 1799)), delivery.ImagePath("")}
		group[1].Source.URL = "https://example.org/image.png"
		group[0].Delivery.HookPoint = "test"
		r, err := a.sendOutputs(t.Context(), sendTarget{Kind: targetGroup, OpenID: "g"}, group)
		if len(msgs) != 2 || (err != nil) != failSecond || len(r.SentMessages) == 0 || len(r.SentMessages[0].OutputIndexes) != 0 || msgs[0].Markdown.ForceVerifyImageResource || !msgs[1].Markdown.ForceVerifyImageResource || !strings.Contains(msgs[1].Markdown.Content, group[1].Source.URL) {
			t.Fatalf("pages=%+v receipt=%+v err=%v", msgs, r, err)
		}
		if len(h.rows) != len(r.SentMessages) || !strings.HasSuffix(h.rows[0].Text, "（1/2）") {
			t.Fatal("wrong page history")
		}
		if failSecond {
			if len(r.SentMessages) != 1 {
				t.Fatal(r)
			}
		} else if len(r.SentMessages) != 2 || !slices.Equal(r.SentMessages[1].OutputIndexes, []int{1}) {
			t.Fatal(r)
		}
	}
}

func TestPagedKeyboardAndNewline(t *testing.T) {
	var msgs []messageToCreate
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m messageToCreate
		_ = json.NewDecoder(r.Body).Decode(&m)
		msgs = append(msgs, m)
		fmt.Fprint(w, `{"id":"sent"}`)
	}))
	defer srv.Close()
	a := newQQOfficialSendTestAdapter(t, srv)
	enabled := true
	a.cfg.MarkdownByDefault = &enabled
	a.cfg.EnableKeyboard = &enabled
	out := delivery.Text("header\n" + strings.Repeat("字", 1800))
	out.Actions = []delivery.Action{{ID: "yes", Label: "确认", Command: "/confirm"}}
	_, err := a.sendOutputs(t.Context(), sendTarget{Kind: targetC2C, OpenID: "u", commandReply: true}, []delivery.Output{out})
	if err != nil || len(msgs) != 2 || msgs[0].Keyboard == nil || msgs[1].Keyboard != nil || msgs[0].Markdown.Content != "header\n\n（1/2）" || msgs[1].Markdown.Content != strings.Repeat("字", 1800)+"\n（2/2）" {
		t.Fatalf("messages=%+v err=%v", msgs, err)
	}
}
func TestGroupPreparationBeforeUploadAndSequence(t *testing.T) {
	var paths []string
	var sent messageToCreate
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/messages") {
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Error(err)
			}
			fmt.Fprint(w, `{"id":"sent"}`)
			return
		}
		http.Error(w, "prepare failed", http.StatusBadRequest)
	}))
	defer server.Close()
	a := newQQOfficialSendTestAdapter(t, server)
	target := sendTarget{Kind: targetGroup, OpenID: "g", MsgID: "incoming"}
	image := delivery.Output{Kind: delivery.KindImage, Source: delivery.Source{Data: []byte("\x89PNG\r\n\x1a\nbody")}}
	if _, err := a.sendOutputs(t.Context(), target, []delivery.Output{delivery.Text("caption"), {Kind: delivery.KindImage, Source: delivery.Source{URL: "https://example.com/" + strings.Repeat("a", 1800)}}}); err == nil || len(paths) != 0 {
		t.Fatalf("oversized group: paths=%v err=%v", paths, err)
	}
	if _, err := a.sendOutputs(t.Context(), target, []delivery.Output{image, delivery.Text("caption")}); err == nil {
		t.Fatal("expected upload failure")
	}
	if _, err := a.sendOutputs(t.Context(), target, []delivery.Output{delivery.Text("retry")}); err != nil {
		t.Fatal(err)
	}
	if sent.MsgSeq != 1 || sent.MsgID != "incoming" || !slices.Equal(paths, []string{"/v2/media-images/prepare", "/v2/groups/g/messages"}) {
		t.Fatalf("msg=%+v paths=%v", sent, paths)
	}
}
func TestGroupedOutputReceipt(t *testing.T) {
	var messages []messageToCreate
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/files") {
			fmt.Fprint(w, `{"file_info":"file"}`)
			return
		}
		var msg messageToCreate
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Error(err)
		}
		messages = append(messages, msg)
		fmt.Fprint(w, `{"id":"sent"}`)
	}))
	defer server.Close()
	a := newQQOfficialSendTestAdapter(t, server)
	group := []delivery.Output{delivery.Text("before"), {Kind: delivery.KindImage, Source: delivery.Source{URL: "https://example.com/a.png"}}, delivery.Text("after")}
	target := delivery.Target{ChatType: "group", ChatID: "9"}
	history := &captureGroupHistory{interfaceHistory: a.chatHistory}
	a.chatHistory = history
	group[0].Delivery.HookPoint = "platform.message.received"
	receipt, err := a.SendTo(t.Context(), target, group)
	if err != nil || len(messages) != 1 || messages[0].Markdown == nil || !strings.HasPrefix(messages[0].Markdown.Content, "before") || !strings.Contains(messages[0].Markdown.Content, group[1].Source.URL) || !strings.HasSuffix(messages[0].Markdown.Content, "after") || len(receipt.SentMessages) != 1 || !slices.Equal(receipt.SentMessages[0].OutputIndexes, []int{1}) {
		t.Fatalf("group: messages=%+v receipt=%+v err=%v", messages, receipt, err)
	}
	if len(history.rows) != 1 || history.rows[0].PlatformMessageID != "sent" || history.rows[0].Text != "beforeafter" {
		t.Fatalf("history=%+v", history.rows)
	}
	if !messages[0].Markdown.ForceVerifyImageResource {
		t.Fatal("missing image verification")
	}
	group[0].Delivery.HookPoint = ""
	for _, out := range group[:2] {
		if _, err := a.SendTo(t.Context(), target, []delivery.Output{out}); err != nil {
			t.Fatal(err)
		}
	}
	if len(messages) != 3 || messages[2].Media == nil {
		t.Fatalf("independent calls: %+v", messages)
	}
}
