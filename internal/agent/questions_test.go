package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/platform/cli/remote/management"
	"elbot/internal/storage"
	"elbot/internal/testutil"
)

type questionsPlatform struct {
	PlatformCapabilities
	posted  chan delivery.Output
	release chan struct{}
}

func (p *questionsPlatform) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (operationresult.Result[delivery.Receipt], error) {
	for _, out := range outputs {
		if len(out.Blocks) > 0 && out.Blocks[0].Kind == delivery.ContentQuestion {
			select {
			case p.posted <- out:
			case <-ctx.Done():
				return operationresult.Result[delivery.Receipt]{}, ctx.Err()
			}
			select {
			case <-p.release:
			case <-ctx.Done():
				return operationresult.Result[delivery.Receipt]{}, ctx.Err()
			}
		}
	}
	return p.PlatformCapabilities.SendTo(ctx, target, outputs)
}

const questionArgs = `{"questions":[{"question":"哪些测试？","options":["相关包","全量"]},{"question":"如何提交？","options":["先审阅"]},{"question":"分支？","options":["本地"]},{"question":"部署？","options":["稍后"]}]}`

func TestQuestionSubmission(t *testing.T) {
	for _, route := range []string{"chat", "response"} {
		t.Run(route, func(t *testing.T) {
			deps := isolatedModuleDeps(t)
			connectTestNotifications(t, deps.Platform.(isolatedPlatformCapabilities).Router)
			chat := &contractChat{call: llm.ToolCallRequest{Name: "ask_user", Arguments: questionArgs}}
			responseInputs := make(chan []json.RawMessage, 1)
			if route == "chat" {
				contractModels(t, &deps, chat)
			} else {
				call := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					call++
					if call == 2 {
						var body struct{ Input []json.RawMessage }
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						responseInputs <- body.Input
					}
					raw := fmt.Sprintf(`{"id":"r%d","status":"completed","output":[{"type":"message","id":"m%d","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}]}`, call, call)
					if call == 1 {
						raw = fmt.Sprintf(`{"id":"r1","status":"completed","output":[{"type":"function_call","id":"f1","call_id":"ask","name":"ask_user","arguments":%q,"status":"completed"}]}`, questionArgs)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", raw)
				}))
				t.Cleanup(server.Close)
				models, err := modelmgr.NewModelManager(t.Context(), &config.Config{Providers: map[string]config.ProviderConfig{"p": {BaseURL: server.URL, APIMode: "response", Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, LLMRequest: config.LLMRequestConfig{FirstChunkTimeoutSeconds: 10, StreamIdleTimeoutSeconds: 10}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = models.Close(context.Background()) })
				deps.Models = models
			}
			platform := &questionsPlatform{deps.Platform, make(chan delivery.Output, 1), make(chan struct{})}
			deps.Platform = platform
			m, err := NewSupervisor(t.Context(), isolatedAgentConfig(), deps)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close(context.Background()) })
			if err := m.Start(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 8*time.Second)
			defer cancel()
			ctx = contextinfo.WithActor(ctx, m.identity.Actor(ctx))
			send := func(text string) error { return m.HandleMessage(ctx, MessageRequest{Text: text}) }
			done := make(chan error, 1)
			go func() { done <- send("@tool:ask_user 请提问") }()
			var out delivery.Output
			select {
			case out = <-platform.posted:
			case err := <-done:
				t.Fatalf("no question sent: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if len(out.Actions) != 6 || !strings.Contains(out.Text, "/ac") {
				t.Fatal(out)
			}
			wire, err := json.Marshal(out.Blocks)
			if err != nil {
				t.Fatal(err)
			}
			var blocks []delivery.ContentBlock
			if err := json.Unmarshal(wire, &blocks); err != nil || delivery.ContentBlocksText(blocks) != out.Text {
				t.Fatalf("question blocks did not round trip: %s, %v", wire, err)
			}
			first := blocks[0].Content.(delivery.QuestionContent)
			second := out.Blocks[1].Content.(delivery.QuestionContent)
			queued := out.Blocks[2].Content.(delivery.QuestionContent)
			third := out.Blocks[3].Content.(delivery.QuestionContent)
			sid := first.SessionID
			// Use real HTTP routes and slash commands against the same supervisor and SQLite database.
			api := management.NewHandler(management.HandlerOptions{Control: deps.Control, Authenticate: func(_ context.Context, r *http.Request) (context.Context, bool, error) {
				return ctx, r.Header.Get("X-CLI-Connection") == "connected", nil
			}, Continue: func(ctx context.Context, c control.Continuation) {
				_, res, err := m.ContinueControlInput(ctx, c)
				if err != nil || res.Status != operationresult.Success {
					t.Fatalf("continue: %+v %v", res, err)
				}
			}})
			request := func(method, path, body string, connected bool, code int) string {
				t.Helper()
				req := httptest.NewRequest(method, "/api/v1/sessions/"+path, strings.NewReader(body))
				if connected {
					req.Header.Set("X-CLI-Connection", "connected")
				}
				w := httptest.NewRecorder()
				api.ServeHTTP(w, req)
				if w.Code != code {
					t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
				}
				return w.Body.String()
			}
			draftPath := fmt.Sprintf("%s/questions/%d/draft", sid, first.ID)
			request("PUT", draftPath, `{"choice":1,"text":"旧补充"}`, false, 200)
			if err := send(fmt.Sprintf("/a %d 2 跑竞态", first.ID)); err != nil {
				t.Fatal(err)
			}
			request("PUT", fmt.Sprintf("%s/questions/%d/draft", sid, second.ID), `{"text":"我来审阅"}`, false, 200)
			request("PUT", draftPath, `{"choice":9}`, false, 400)
			request("POST", sid+"/questions/confirm", "", false, 409)
			request("POST", "wrong-session/questions/confirm", "", true, 409)
			response := request("POST", sid+"/questions/confirm", "", true, 200)
			var confirmed control.ConfirmAnswersResult
			if err := json.Unmarshal([]byte(response), &confirmed); err != nil || !slices.Equal(confirmed.QuestionIDs, []int64{first.ID, second.ID}) {
				t.Fatalf("confirm %s %v", response, err)
			}
			request("POST", sid+"/questions/confirm", "", true, 400)
			request("PUT", draftPath, `{"choice":2,"text":"队中改选"}`, false, 200)
			request("PUT", fmt.Sprintf("%s/questions/%d/draft", sid, queued.ID), `{"text":"独立批次"}`, false, 200)
			request("POST", sid+"/questions/confirm", "", true, 200)
			close(platform.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			var texts []string
			if route == "chat" {
				if len(chat.requests) != 2 {
					t.Fatal("model calls", len(chat.requests))
				}
				for _, msg := range chat.requests[1].Messages {
					if msg.Role == llm.RoleUser {
						texts = append(texts, llm.SegmentsTextOnly(msg.Segments))
					}
				}
			} else {
				for _, raw := range <-responseInputs {
					var item struct{ Role string }
					_ = json.Unmarshal(raw, &item)
					if item.Role == "user" {
						texts = append(texts, string(raw))
					}
				}
			}
			if len(texts) < 2 || !strings.Contains(texts[len(texts)-2], "队中改选") || strings.Contains(texts[len(texts)-2], "独立批次") || !strings.Contains(texts[len(texts)-1], "独立批次") {
				t.Fatalf("next request: %v", texts)
			}
			got := request("GET", sid+"/questions", "", false, 200)
			var remaining struct{ Items []storage.Question }
			if err := json.Unmarshal([]byte(got), &remaining); err != nil || len(remaining.Items) != 1 || remaining.Items[0].ID != third.ID {
				t.Fatal(got, err)
			}
			if err := send(fmt.Sprintf("/a %d o 待商量", third.ID)); err != nil {
				t.Fatal(err)
			}
			if err := send("/new"); err != nil {
				t.Fatal(err)
			}
			if err := send("新会话消息"); err != nil {
				t.Fatal(err)
			}
			request("GET", sid+"/questions", "", false, 409)
			if err := send("/resume " + sid); err != nil {
				t.Fatal(err)
			}
			if err := send("先看其他文件"); err != nil {
				t.Fatal(err)
			}
			restored := request("GET", sid+"/questions", "", false, 200)
			if !strings.Contains(restored, "待商量") {
				t.Fatalf("draft lost across session switch: %s", restored)
			}
			reserve := func() *control.Continuation {
				t.Helper()
				result, err := deps.Control.ConfirmAnswers(ctx, sid)
				if err != nil || result.Status != operationresult.Success || result.Data.Continuation == nil {
					t.Fatalf("idle confirm: %+v %v", result, err)
				}
				return result.Data.Continuation
			}
			canceled := reserve()
			duplicate, err := deps.Control.ConfirmAnswers(ctx, sid)
			if err != nil || duplicate.Status != operationresult.Rejected {
				t.Fatalf("duplicate reservation: %+v %v", duplicate, err)
			}
			canceled.CancelReservation()
			stale := reserve()
			if err := send(fmt.Sprintf("/a %d o 明天部署", third.ID)); err != nil {
				t.Fatal(err)
			}
			stale.CancelReservation()
			stopped := reserve()
			if err := send("/stop"); err != nil {
				t.Fatal(err)
			}
			_, conflict, err := m.ContinueControlInput(ctx, *stopped)
			if err != nil || conflict.Status != operationresult.Conflict {
				t.Fatalf("stopped submission: %+v %v", conflict, err)
			}
			if err := send("/ac"); err != nil {
				t.Fatal(err)
			}
			history, err := deps.Store.Messages().ListBySession(ctx, sid)
			if err != nil {
				t.Fatal(err)
			}
			toolText := "4 题待答"
			answers := fmt.Sprintf("#%d 哪些测试？\n选：2. 全量\n补：队中改选\n\n#%d 如何提交？\n答：我来审阅", first.ID, second.ID)
			for _, want := range []struct{ role, text string }{{"tool", toolText}, {"user", "先看其他文件"}, {"user", answers}, {"user", fmt.Sprintf("#%d 分支？\n答：独立批次", queued.ID)}, {"user", fmt.Sprintf("#%d 部署？\n答：明天部署", third.ID)}} {
				count := 0
				for _, msg := range history {
					if string(msg.Role) == want.role && strings.Contains(msg.Content, want.text) {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("wanted exactly one %s %q; history=%+v", want.role, want.text, history)
				}
			}
		})
	}
}

// Hold the final reply in flight so /ac must queue without cancelling the LLM.
type waitingQuestionChat struct {
	contractChat
	calling chan struct{}
	release chan struct{}
}

func (c *waitingQuestionChat) Stream(ctx context.Context, r chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	ch, err := c.contractChat.Stream(ctx, r)
	if len(c.requests) != 2 {
		return ch, err
	}
	close(c.calling)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.release:
		return ch, err
	}
}
func TestQuestionSubmissionDuringLLM(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop=%t", stop), func(t *testing.T) {
			deps := isolatedModuleDeps(t)

			chat := &waitingQuestionChat{contractChat: contractChat{call: llm.ToolCallRequest{Name: "ask_user", Arguments: questionArgs}}, calling: make(chan struct{}), release: make(chan struct{})}
			contractModels(t, &deps, chat)
			m := contractSupervisor(t, deps)
			ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 8*time.Second)
			defer cancel()
			ctx = contextinfo.WithActor(ctx, m.identity.Actor(ctx))
			send := func(text string) error { return m.HandleMessage(ctx, MessageRequest{Text: text}) }
			done := make(chan error, 1)
			go func() { done <- send("@tool:ask_user 请提问") }()
			select {
			case <-chat.calling:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			qs, err := deps.Control.ListQuestions(ctx, "")
			if err != nil || len(qs.Data) < 2 {
				t.Fatal(qs, err)
			}
			sid := qs.Data[0].SessionID
			for i, q := range qs.Data[:2] {
				if err := send(fmt.Sprintf("/a %d o answer-%d", q.ID, i)); err != nil {
					t.Fatal(err)
				}
				if err := send("/ac"); err != nil {
					t.Fatal(err)
				}
			}
			if stop {
				if err := send("/stop"); err != nil {
					t.Fatal(err)
				}
			}
			close(chat.release)
			if err := <-done; err != nil && !(stop && errors.Is(err, context.Canceled)) {
				t.Fatal(err)
			}
			messages := func() []string {
				t.Helper()
				history, err := deps.Store.Messages().ListBySession(ctx, sid)
				if err != nil {
					t.Fatal(err)
				}
				var found []string
				for _, msg := range history {
					if msg.Role == storage.RoleUser && strings.Contains(msg.Content, "answer-") {
						found = append(found, msg.Content)
					}
				}
				return found
			}
			if stop {
				if found := messages(); len(found) != 0 {
					t.Fatalf("stopped answers persisted: %v", found)
				}
				if err := send("/ac"); err != nil {
					t.Fatal(err)
				}
				if found := messages(); len(found) != 1 || !strings.Contains(found[0], "answer-0") || !strings.Contains(found[0], "answer-1") {
					t.Fatal(found)
				}
			} else {
				found := messages()
				if len(chat.requests) != 3 || len(found) != 2 || !strings.Contains(found[0], "answer-0") || !strings.Contains(found[1], "answer-1") {
					t.Fatal(len(chat.requests), found)
				}
			}
			remaining, err := deps.Control.ListQuestions(ctx, sid)
			if err != nil || len(remaining.Data) != len(qs.Data)-2 {
				t.Fatal(remaining, err)
			}
		})
	}
}
