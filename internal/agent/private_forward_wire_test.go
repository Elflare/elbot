package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"elbot/internal/llm/openai"
	"elbot/internal/media"
	qqonebot "elbot/internal/platform/qq-onebot"
	"elbot/internal/storage/sqlite"
)

type forwardWireHandler func(context.Context, string) error

func (h forwardWireHandler) HandleMessage(ctx context.Context, text string) error {
	return h(ctx, text)
}

func TestPrivateOneBotForwardReachesVendor(t *testing.T) {
	for _, withImage := range []bool{false, true} {
		t.Run(fmt.Sprintf("image=%v", withImage), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store := newTestStore(t)
			history, err := sqlite.NewChatHistory(ctx, filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer history.Close()
			var picture bytes.Buffer
			if err := png.Encode(&picture, image.NewNRGBA(image.Rect(0, 0, 8, 8))); err != nil {
				t.Fatal(err)
			}
			requests := make(chan []byte, 4)
			vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/image.png" {
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(picture.Bytes())
					return
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read vendor request: %v", err)
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer vendor.Close()
			imageSegment := ""
			if withImage {
				imageSegment = fmt.Sprintf(`,{"type":"image","data":{"file":"inside.png","url":%q}}`, vendor.URL+"/image.png")
			}
			// Match the real service's messages[].message response shape.
			forwardResponse := json.RawMessage(`{"messages":[{"sender":{"nickname":"Elflare"},"message":[{"type":"text","data":{"text":"第一条正文\n保留换行"}}` + imageSegment + `]},{"sender":{"nickname":"Elflare"},"message":[{"type":"text","data":{"text":"第二条正文"}}]}]}`)
			onebot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Errorf("accept onebot: %v", err)
					return
				}
				defer conn.CloseNow()
				event := json.RawMessage(`{"post_type":"message","message_type":"private","self_id":1000,"user_id":1,"message_id":123,"message":[{"type":"forward","data":{"id":"secret-resource"}}]}`)
				if err := wsjson.Write(ctx, conn, event); err != nil {
					t.Errorf("write event: %v", err)
					return
				}
				for {
					var req struct {
						Action string         `json:"action"`
						Params map[string]any `json:"params"`
						Echo   string         `json:"echo"`
					}
					if err := wsjson.Read(ctx, conn, &req); err != nil {
						return
					}
					data := json.RawMessage(`{"message_id":999}`)
					switch req.Action {
					case "get_forward_msg":
						if req.Params["message_id"] != "secret-resource" {
							t.Errorf("resource=%v", req.Params)
						}
						data = forwardResponse
					case "send_private_msg":
					default:
						t.Errorf("unexpected API %s", req.Action)
					}
					if err := wsjson.Write(ctx, conn, map[string]any{"status": "ok", "retcode": 0, "data": data, "echo": req.Echo}); err != nil {
						return
					}
				}
			}))
			defer onebot.Close()
			root := t.TempDir()
			a := newTestMediaAgent(t, &fakePlatform{}, openai.New(vendor.URL, "test", nil), store, media.NewManager(store, root, &media.LocalBackend{Root: root}))
			a.message.media.History = history.Repository()
			adapter := qqonebot.New(qqonebot.Config{Enabled: true, URL: "ws" + strings.TrimPrefix(onebot.URL, "http")}, store, history.Repository(), nil)
			completed := make(chan error, 1)
			runDone := make(chan error, 1)
			go func() {
				runDone <- adapter.Run(ctx, forwardWireHandler(func(inputCtx context.Context, text string) error {
					err := a.HandleMessage(inputCtx, text)
					completed <- err
					return err
				}))
			}()
			defer func() {
				cancel()
				select {
				case <-runDone:
				case <-time.After(time.Second):
					t.Error("adapter did not stop")
				}
			}()
			select {
			case err := <-completed:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var captured []byte
			select {
			case captured = <-requests:
			default:
				t.Fatal("no vendor request")
			}
			var request struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(captured, &request); err != nil {
				t.Fatal(err)
			}
			var visible strings.Builder
			imageCount := 0
			for _, message := range request.Messages {
				if message.Role != "user" {
					continue
				}
				var plain string
				if json.Unmarshal(message.Content, &plain) == nil {
					visible.WriteString(plain)
					continue
				}
				var parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if err := json.Unmarshal(message.Content, &parts); err != nil {
					t.Fatal(err)
				}
				for _, part := range parts {
					if part.Type == "image_url" {
						imageCount++
						visible.WriteString("{image}")
					} else {
						visible.WriteString(part.Text)
					}
				}
			}
			text := visible.String()
			ordered := []string{"<forward_message>", "Elflare：第一条正文\n保留换行", "Elflare：第二条正文", "</forward_message>"}
			if withImage {
				ordered = []string{ordered[0], ordered[1], "{image}", ordered[2], ordered[3]}
			}
			rest := text
			for _, token := range ordered {
				_, after, found := strings.Cut(rest, token)
				if !found {
					t.Fatalf("missing/out-of-order %q in %q", token, text)
				}
				rest = after
			}
			if strings.Contains(text, "[forward]") || strings.Contains(text, "secret-resource") || strings.Count(text, "第一条正文") != 1 || (withImage && imageCount != 1) || (!withImage && imageCount != 0) {
				t.Fatalf("unexpected vendor content=%q images=%d", text, imageCount)
			}
			row, err := history.Repository().GetByPlatformMessage(ctx, "qqonebot", "private:1", "123")
			if err != nil || row.Text != "[forward]" || strings.Contains(row.Segments, "inside.png") {
				t.Fatalf("raw history changed: %#v, %v", row, err)
			}
			associations, err := store.Media().FindHistory(ctx, "qqonebot", "private:1", "123")
			if err != nil || len(associations) != 0 {
				t.Fatalf("internal image has top-level history index: %#v, %v", associations, err)
			}
		})
	}
}
