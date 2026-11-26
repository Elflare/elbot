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
	"elbot/internal/request"
	"elbot/internal/storage"
	"elbot/internal/testutil"
	"elbot/internal/turn"
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

func TestConfirmBeforeFirstQuestion(t *testing.T) {
	var questions storage.QuestionSet
	if ids := questions.Submit("s"); len(ids) != 0 {
		t.Fatalf("unexpected answers: %v", ids)
	}
}
func TestQuestionPresentation(t *testing.T) {
	for _, action := range []string{"submit", "cancel", "submit-together"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 5*time.Second)
			defer cancel()
			chat := &mixedAsyncChat{}
			for _, id := range []string{"first", "second"} {
				chat.calls = append(chat.calls, chatcompletions.ToolCallDelta{ID: id, Name: "ask_user", Args: questionArgs})
			}
			deps := isolatedModuleDeps(t)
			contractModels(t, &deps, chat)
			connectTestNotifications(t, deps.Platform.(isolatedPlatformCapabilities).Router)
			p := &questionsPlatform{deps.Platform, make(chan delivery.Output, 2), make(chan struct{})}
			deps.Platform = p
			m, err := NewSupervisor(t.Context(), isolatedAgentConfig(), deps)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close(context.Background()) })
			defer close(p.release)
			if err := m.Start(); err != nil {
				t.Fatal(err)
			}
			ctx = contextinfo.WithActor(ctx, m.identity.Actor(ctx))
			done := make(chan error, 1)
			go func() { done <- m.HandleMessage(ctx, MessageRequest{Text: "@tool:ask_user ask"}) }()
			posted := func() delivery.QuestionContent {
				t.Helper()
				select {
				case out := <-p.posted:
					return out.Blocks[0].Content.(delivery.QuestionContent)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
					return delivery.QuestionContent{}
				}
			}
			first := posted()
			if action == "cancel" {
				for _, r := range m.requests.ListBySession(first.SessionID) {
					if r.Kind == request.KindTurn {
						m.requests.Cancel(r.ID)
					}
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("turn cancellation did not interrupt presentation")
				}
				if jobs := m.requests.ListBySession(first.SessionID); len(jobs) != 0 {
					t.Fatalf("unaccepted jobs: %+v", jobs)
				}
				return
			}
			p.release <- struct{}{}
			second := posted()
			if action == "submit-together" {
				p.release <- struct{}{}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				before := len(chat.requests)
				if r, err := deps.Control.ConfirmAnswers(ctx, first.SessionID); err != nil || len(r.Data.QuestionIDs) != 8 {
					t.Fatal(r, err)
				}
				for {
					rows, err := deps.Store.ListSessionMessages(ctx, first.SessionID)
					if err != nil {
						t.Fatal(err)
					}
					reports := 0
					for _, row := range rows {
						if row.Role == llm.RoleTool && row.ToolName == "ask_user" && strings.Count(row.Content, "未回答") == 4 {
							reports++
						}
					}
					if reports == 2 && len(m.requests.ListBySession(first.SessionID)) == 0 && m.turns.Snapshot(first.SessionID).Phase == turn.PhaseIdle {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(time.Millisecond):
					}
				}
				audits, err := deps.Store.ListSessionToolCalls(ctx, first.SessionID)
				calls := len(chat.requests) - before
				if err != nil || len(audits) != 2 || calls < 1 || calls > 2 {
					t.Fatal(calls, audits, err)
				}
				t.Logf("two groups, eight questions: %d model requests after /ac, 2 report pairs, %d audits", calls, len(audits))
				return
			}
			qs, err := deps.Control.ListQuestions(ctx, first.SessionID)
			if err != nil || len(qs.Data) != 4 || qs.Data[0].ID != first.ID {
				t.Fatalf("unshown group listed: %+v %v", qs, err)
			}
			r, err := deps.Control.ConfirmAnswers(ctx, first.SessionID)
			if err != nil || !slices.Equal(r.Data.QuestionIDs, []int64{first.ID, first.ID + 1, first.ID + 2, first.ID + 3}) {
				t.Fatalf("submitted unseen questions: %+v %v", r, err)
			}
			p.release <- struct{}{}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			qs, err = deps.Control.ListQuestions(ctx, first.SessionID)
			if err != nil || len(qs.Data) != 4 || qs.Data[0].ID != second.ID {
				t.Fatalf("second group lost: %+v %v", qs, err)
			}
		})
	}
}
func TestSynchronousQuestionResult(t *testing.T) {
	deps := isolatedModuleDeps(t)
	connectTestNotifications(t, deps.Platform.(isolatedPlatformCapabilities).Router)
	chat := &contractChat{call: llm.ToolCallRequest{Name: "ask_user", Arguments: `{"questions":[{"question":"One?","options":["Yes"]},{"question":"Two?","options":["No"]}],"async":false}`}}
	contractModels(t, &deps, chat)
	platform := &questionsPlatform{PlatformCapabilities: deps.Platform, posted: make(chan delivery.Output, 1), release: make(chan struct{})}
	close(platform.release)
	deps.Platform = platform
	m, err := NewSupervisor(t.Context(), isolatedAgentConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 5*time.Second)
	defer cancel()
	ctx = contextinfo.WithActor(ctx, m.identity.Actor(ctx))
	send := func(text string) error { return m.HandleMessage(ctx, MessageRequest{Text: text}) }
	done := make(chan error, 1)
	go func() { done <- send("@tool:ask_user ask me") }()
	select {
	case <-platform.posted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// posted signals entry to SendTo, not its successful return.
	var qs []storage.Question
	for len(qs) == 0 {
		r, err := deps.Control.ListQuestions(ctx, "")
		if err != nil || r.Status != operationresult.Success {
			t.Fatal(r, err)
		}
		qs = r.Data
		if len(qs) == 0 {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
	}
	if len(qs) != 2 {
		t.Fatal(qs)
	}
	sid := qs[0].SessionID
	if err := send(fmt.Sprintf("/a %d o sync-answer-0", qs[0].ID)); err != nil {
		t.Fatal(err)
	}
	if err := send("/ac"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rows, err := deps.Store.ListSessionMessages(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range rows {
		if !strings.Contains(row.Content, "sync-answer-") {
			continue
		}
		if row.Role != llm.RoleTool || !strings.Contains(row.Content, "sync-answer-0") || !strings.Contains(row.Content, "Two?\n未回答") {
			t.Fatalf("misrouted answer: %+v", row)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("got %d answer messages", count)
	}
}
func TestQuestionSubmission(t *testing.T) {
	for _, route := range []string{"chat", "response"} {
		t.Run(route, func(t *testing.T) {
			deps := isolatedModuleDeps(t)
			connectTestNotifications(t, deps.Platform.(isolatedPlatformCapabilities).Router)
			chat := &asyncProbeChat{contractChat: contractChat{call: llm.ToolCallRequest{Name: "ask_user", Arguments: questionArgs}}, sent: make(chan chatcompletions.Request, 3)}
			responseInputs := make(chan []json.RawMessage, 1)
			if route == "chat" {
				contractModels(t, &deps, chat)
			} else {
				call := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					call++
					if call == 3 {
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
			if len(out.Actions) != 6 || strings.Count(delivery.OutputBody(out, false), "/ac") != 1 {
				t.Fatal(out)
			}
			wire, err := json.Marshal(out.Blocks)
			if err != nil {
				t.Fatal(err)
			}
			var blocks []delivery.ContentBlock
			if err := json.Unmarshal(wire, &blocks); err != nil || delivery.ContentBlocksText(blocks) != delivery.ContentBlocksText(out.Blocks) {
				t.Fatalf("question blocks did not round trip: %s, %v", wire, err)
			}
			first := blocks[0].Content.(delivery.QuestionContent)
			second := out.Blocks[1].Content.(delivery.QuestionContent)
			sid := first.SessionID
			close(platform.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			} // Receipt returned before /ac.
			if r, err := deps.Control.NewSession(ctx); err != nil || r.Status != operationresult.Busy {
				t.Fatalf("outstanding question did not keep session busy: %+v %v", r, err)
			}
			// Use real HTTP routes and slash commands against the same supervisor and SQLite database.
			api := management.NewHandler(management.HandlerOptions{Control: deps.Control, Authenticate: func(context.Context, *http.Request) (context.Context, error) {
				return ctx, nil
			}})
			request := func(method, path, body string, code int) string {
				t.Helper()
				req := httptest.NewRequest(method, "/api/v1/sessions/"+path, strings.NewReader(body))
				w := httptest.NewRecorder()
				api.ServeHTTP(w, req)
				if w.Code != code {
					t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
				}
				return w.Body.String()
			}
			draftPath := fmt.Sprintf("%s/questions/%d/draft", sid, first.ID)
			request("PUT", draftPath, `{"choice":1,"text":"旧补充"}`, 200)
			if err := send(fmt.Sprintf("/a %d 2 跑竞态", first.ID)); err != nil {
				t.Fatal(err)
			}
			request("PUT", fmt.Sprintf("%s/questions/%d/draft", sid, second.ID), `{"text":"我来审阅"}`, 200)
			request("PUT", draftPath, `{"choice":9}`, 400)
			request("POST", "wrong-session/questions/confirm", "", 409)
			response := request("POST", sid+"/questions/confirm", "", 200)
			var confirmed control.ConfirmAnswersResult
			if err := json.Unmarshal([]byte(response), &confirmed); err != nil || !slices.Equal(confirmed.QuestionIDs, []int64{first.ID, second.ID, blocks[2].Content.(delivery.QuestionContent).ID, blocks[3].Content.(delivery.QuestionContent).ID}) {
				t.Fatalf("confirm %s %v", response, err)
			}
			request("POST", sid+"/questions/confirm", "", 400)
			got := request("GET", sid+"/questions", "", 200)
			var remaining struct{ Items []storage.Question }
			if err := json.Unmarshal([]byte(got), &remaining); err != nil || len(remaining.Items) != 0 {
				t.Fatal(got, err)
			}
			var reportID, text string
			if route == "chat" {
				var r chatcompletions.Request
				for range 3 {
					select {
					case r = <-chat.sent:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				msgs := r.Messages
				head, out := msgs[len(msgs)-2], msgs[len(msgs)-1]
				if head.Role != llm.RoleAssistant || len(head.ToolCalls) != 1 || head.ToolCalls[0].Name != "ask_user" || head.ToolCalls[0].Arguments != questionArgs || head.ToolCalls[0].ID == "contract-call" || head.ToolCalls[0].ID != out.ToolCallID || out.Role != llm.RoleTool {
					t.Fatalf("invalid report pair: %+v %+v", head, out)
				}
				reportID, text = out.ToolCallID, llm.SegmentsTextOnly(out.Segments)
			} else {
				var inputs []json.RawMessage
				select {
				case inputs = <-responseInputs:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				var head, out struct {
					Type, Name, Arguments string
					CallID                string `json:"call_id"`
					Output                string
				}
				if len(inputs) < 2 {
					t.Fatal(inputs)
				}
				if err := json.Unmarshal(inputs[len(inputs)-2], &head); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(inputs[len(inputs)-1], &out); err != nil {
					t.Fatal(err)
				}
				if head.Type != "function_call" || head.Name != "ask_user" || head.Arguments != questionArgs || head.CallID == "ask" || head.CallID != out.CallID || out.Type != "function_call_output" {
					t.Fatalf("invalid native pair: %+v %+v", head, out)
				}
				reportID, text = out.CallID, out.Output
			}
			if !strings.Contains(text, "选：2. 全量\n补：跑竞态") || !strings.Contains(text, "答：我来审阅") || strings.Count(text, "未回答") != 2 || strings.Contains(text, "\n\n#") {
				t.Fatal(text)
			}
			history, err := deps.Store.ListSessionMessages(ctx, sid)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, m := range history {
				if strings.Contains(m.Content, "我来审阅") {
					if m.Role != llm.RoleTool || m.ToolCallID != reportID {
						t.Fatalf("misrouted answer: %+v", m)
					}
					count++
				}
			}
			audits, err := deps.Store.ListSessionToolCalls(ctx, sid)
			if err != nil || len(audits) != 1 || count != 1 {
				t.Fatalf("reports=%d audits=%+v err=%v", count, audits, err)
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
	for _, action := range []string{"submit", "session-stop", "single-stop"} {
		t.Run(action, func(t *testing.T) {
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
			command, want := "/ac", "未回答"
			if action == "single-stop" {
				for _, job := range m.requests.ListBySession(sid) {
					if job.Label == "ask_user" {
						command, want = "/stop "+job.ID, job.ID+" 被用户取消"
					}
				}
				if command == "/ac" {
					t.Fatal("missing ask_user job")
				}
			}
			if err := send(command); err != nil {
				t.Fatal(err)
			}
			for m.turns.Snapshot(sid).PendingCount == 0 {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(time.Millisecond):
				}
			}
			if action == "session-stop" {
				if err := send("/stop"); err != nil {
					t.Fatal(err)
				}
			}
			close(chat.release)
			if err := <-done; err != nil && !(action == "session-stop" && errors.Is(err, context.Canceled)) {
				t.Fatal(err)
			}
			messages := func() []string {
				t.Helper()
				history, err := deps.Store.ListSessionMessages(ctx, sid)
				if err != nil {
					t.Fatal(err)
				}
				var found []string
				for _, msg := range history {
					if msg.Role == llm.RoleTool && msg.ToolName == "ask_user" && msg.ToolCallID != "contract-call" {
						found = append(found, msg.Content)
					}
				}
				return found
			}
			if action == "session-stop" {
				if found := messages(); len(found) != 0 {
					t.Fatalf("stopped answers persisted: %v", found)
				}
				if err := send("/ac"); err != nil {
					t.Fatal(err)
				}
				if found := messages(); len(found) != 0 {
					t.Fatalf("stopped answers replayed: %v", found)
				}
			} else {
				found := messages()
				if len(chat.requests) != 3 || len(found) != 1 || !strings.Contains(found[0], want) || (action == "submit" && strings.Count(found[0], want) != len(qs.Data)) {
					t.Fatal(len(chat.requests), found)
				}
			}
			remaining, err := deps.Control.ListQuestions(ctx, sid)
			if err != nil || len(remaining.Data) != 0 {
				t.Fatal(remaining, err)
			}
		})
	}
}
