package agent

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	"elbot/internal/testutil"
	"elbot/internal/turn"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAsyncCompletionResponses(t *testing.T) {
	for _, store := range []bool{false, true} {
		t.Run(fmt.Sprint(store), func(t *testing.T) {
			dir := t.TempDir()
			release := filepath.Join(dir, "release")
			args, _ := json.Marshal(map[string]any{"async": true, "cmd": fmt.Sprintf("while [ ! -f %q ]; do sleep 0.03; done; printf fresh-result", release)})
			var seq atomic.Int32
			arrived := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := seq.Add(1)
				var body struct {
					Previous string `json:"previous_response_id"`
					Input    []struct {
						Type, Name, Arguments string
						CallID                string `json:"call_id"`
						Output                json.RawMessage
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				prev := ""
				if store && n > 1 {
					prev = fmt.Sprintf("resp_%d", n-1)
				}
				if body.Previous != prev {
					t.Errorf("request %d previous=%q want %q", n, body.Previous, prev)
				}
				output := `[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]`
				if n == 1 {
					output = fmt.Sprintf(`[{"type":"function_call","call_id":"call_A","name":"shell","arguments":%q}]`, args)
				}
				if n == 4 || n == 5 && !store {
					found := false
					for i, x := range body.Input {
						if x.Type == "function_call" && x.CallID != "call_A" && i+1 < len(body.Input) {
							next := body.Input[i+1]
							found = next.Type == "function_call_output" && next.CallID == x.CallID && strings.Contains(string(next.Output), "fresh-result")
						}
					}
					if !found {
						t.Errorf("request %d missing report pair: %+v", n, body.Input)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%d\",\"store\":%t,\"status\":\"completed\",\"output\":%s}}\n\n", n, store, output)
				if n == 4 {
					close(arrived)
				}
			}))
			defer srv.Close()
			deps := isolatedModuleDeps(t)
			models, err := modelmgr.NewModelManager(t.Context(), &config.Config{Providers: map[string]config.ProviderConfig{"p": {APIMode: "response", BaseURL: srv.URL, Models: []string{"m"}, ExtraPayload: map[string]any{"store": store}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, LLMRequest: config.LLMRequestConfig{FirstChunkTimeoutSeconds: 30, StreamIdleTimeoutSeconds: 30}})
			if err != nil {
				t.Fatal(err)
			}
			defer models.Close(context.Background())
			deps.Models = models
			m := contractSupervisor(t, deps)
			ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 6*time.Second)
			defer cancel()
			send := func(s string) error { return m.HandleMessage(ctx, MessageRequest{Text: s}) }
			done := make(chan error, 1)
			go func() { done <- send("@tool:shell test") }()
			for wait := true; wait; {
				select {
				case e := <-done:
					if e != nil {
						t.Fatal(e)
					}
					wait = false
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(5 * time.Millisecond):
					for _, s := range m.turns.SnapshotAll() {
						if s.Phase == turn.PhaseAwaitRiskConfirm {
							if e := send("/confirm"); e != nil {
								t.Fatal(e)
							}
						}
					}
				}
			}
			if err := send("independent turn before completion"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case <-arrived:
			case <-ctx.Done():
				t.Fatal("async Responses call did not arrive")
			}
			for len(m.requests.List()) != 0 {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(5 * time.Millisecond):
				}
			}
			if err := send("follow up"); err != nil {
				t.Fatal(err)
			}
			if seq.Load() != 5 {
				t.Fatalf("got %d requests", seq.Load())
			}
		})
	}
}

type asyncProbeChat struct {
	contractChat
	sent chan chatcompletions.Request
}

func (p *asyncProbeChat) Stream(ctx context.Context, r chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	c, err := p.contractChat.Stream(ctx, r)
	p.sent <- r
	return c, err
}

type mixedAsyncChat struct {
	isolatedClient
	requests []chatcompletions.Request
	calls    []chatcompletions.ToolCallDelta
}

func (c *mixedAsyncChat) Stream(_ context.Context, r chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	c.requests = append(c.requests, r)
	ch := make(chan chatcompletions.Chunk, 1)
	if len(c.requests) == 1 {
		ch <- chatcompletions.Chunk{ToolCallDeltas: c.calls}
	} else {
		ch <- chatcompletions.Chunk{DeltaContent: "done"}
	}
	close(ch)
	return ch, nil
}

// Keep the tool phase open with a synchronous question. Queue an ordinary message,
// two jobs completing in reverse order, and a mixed /ac before the next model call.
func TestAsyncMixedHandoff(t *testing.T) {
	t.Run("deliver", func(t *testing.T) { testAsyncMixedHandoff(t, false) })
	t.Run("stop", func(t *testing.T) { testAsyncMixedHandoff(t, true) })
}
func testAsyncMixedHandoff(t *testing.T, stop bool) {
	ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 8*time.Second)
	defer cancel()
	dir := t.TempDir()
	chat := &mixedAsyncChat{}
	for _, label := range []string{"first", "second"} {
		args, _ := json.Marshal(map[string]any{"async": true, "cmd": fmt.Sprintf("while [ ! -f %q ]; do sleep 0.02; done; printf '%s-result'", filepath.Join(dir, label), label)})
		chat.calls = append(chat.calls, chatcompletions.ToolCallDelta{ID: label, Name: "shell", Args: string(args)})
	}
	for _, async := range []bool{true, false} {
		chat.calls = append(chat.calls, chatcompletions.ToolCallDelta{ID: fmt.Sprint(async), Name: "ask_user", Args: fmt.Sprintf(`{"async":%t,"questions":[{"question":"mode-%t","options":["yes"]}]}`, async, async)})
	}
	deps := isolatedModuleDeps(t)
	contractModels(t, &deps, chat)
	connectTestNotifications(t, deps.Platform.(isolatedPlatformCapabilities).Router)
	platform := &questionsPlatform{deps.Platform, make(chan delivery.Output, 2), make(chan struct{})}
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
	ctx = contextinfo.WithActor(ctx, m.identity.Actor(ctx))
	send := func(s string) {
		t.Helper()
		if err := m.HandleMessage(ctx, MessageRequest{Text: s}); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- m.HandleMessage(ctx, MessageRequest{Text: "@tool:shell @tool:ask_user mixed"}) }()
	var sid string
	for i := 0; i < 2; {
		select {
		case q := <-platform.posted:
			sid = q.Blocks[0].Content.(delivery.QuestionContent).SessionID
			i++
		case <-time.After(5 * time.Millisecond):
			for _, s := range m.turns.SnapshotAll() {
				if s.Phase == turn.PhaseAwaitRiskConfirm {
					send("/confirm")
				}
			}
		case err := <-done:
			t.Fatalf("tool phase ended early: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	send("ordinary-queued")
	for i, label := range []string{"second", "first"} {
		if err := os.WriteFile(filepath.Join(dir, label), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if stop && i == 1 {
			// One report is already queued; the other process is finishing as /stop takes the gate.
			send("/stop")
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			for len(m.requests.ListBySession(sid)) > 0 {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(5 * time.Millisecond):
				}
			}
			if len(chat.requests) != 1 {
				t.Fatalf("stop woke the model: %d requests", len(chat.requests))
			}
			qs, err := deps.Control.ListQuestions(ctx, sid)
			if err != nil || len(qs.Data) != 1 || qs.Data[0].Question != "mode-true" {
				t.Fatalf("synchronous group survived stop: %+v %v", qs, err)
			}
			send("check after stop")
			for _, msg := range chat.requests[1].Messages {
				if msg.Role == llm.RoleTool && strings.Contains(llm.SegmentsTextOnly(msg.Segments), "不是新的执行") {
					t.Fatal("stopped report replayed", msg)
				}
			}
			return
		}
		for m.turns.Snapshot(sid).PendingCount < i+2 {
			select {
			case <-ctx.Done():
				t.Fatalf("job %s did not queue", label)
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	qs, err := deps.Control.ListQuestions(ctx, sid)
	if err != nil || len(qs.Data) != 2 {
		t.Fatal(qs, err)
	}
	for _, q := range qs.Data {
		send(fmt.Sprintf("/a %d o answer-%s", q.ID, q.Question))
	}
	send("/ac")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	send("check replay")
	if len(chat.requests) != 3 {
		t.Fatalf("got %d calls", len(chat.requests))
	}
	// Check adjacency and pairing, not just whether the strings occur somewhere in history.
	check := func(messages []llm.LLMMessage) {
		t.Helper()
		i := slices.IndexFunc(messages, func(m llm.LLMMessage) bool { return m.ToolCallID == "false" })
		if i < 0 || len(messages) < i+7 {
			t.Fatalf("missing synchronous result or deferred input: %+v", messages)
		}
		if !strings.Contains(llm.SegmentsTextOnly(messages[i].Segments), "answer-mode-false") {
			t.Fatalf("sync answer not on its original result")
		}
		for j, label := range []string{"second", "first"} {
			head, out := messages[i+1+2*j], messages[i+2+2*j]
			if len(head.ToolCalls) != 1 || head.ToolCalls[0].ID != out.ToolCallID || out.Role != llm.RoleTool || !strings.Contains(llm.SegmentsTextOnly(out.Segments), label+"-result") {
				t.Fatalf("bad %s pair: %+v %+v", label, head, out)
			}
		}
		for j, want := range []string{"ordinary-queued", "answer-mode-true"} {
			m := messages[i+5+j]
			if m.Role != llm.RoleUser || !strings.Contains(llm.SegmentsTextOnly(m.Segments), want) {
				t.Fatalf("missing %s after reports: %+v", want, m)
			}
		}
	}
	check(chat.requests[1].Messages)
	check(chat.requests[2].Messages)
}
func TestAsyncProcessTool(t *testing.T) {
	for _, name := range []string{"shell", "tool_weave"} {
		for _, stop := range []string{"complete", "failure", "timeout", "single", "session"} {
			t.Run(fmt.Sprintf("%s/%s", name, stop), func(t *testing.T) {
				dir := t.TempDir()
				started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
				args := map[string]any{"async": true}
				if name == "shell" {
					args["cmd"] = fmt.Sprintf("printf x >> %q; while [ ! -f %q ]; do sleep 0.03; done; printf finished", started, release)
				} else {
					args["mode"] = "system"
					args["code"] = fmt.Sprintf("from pathlib import Path\nimport time\nwith Path(%q).open('a') as f: f.write('x')\nwhile not Path(%q).exists(): time.sleep(.03)\nprint('finished')", started, release)
				}
				switch stop {
				case "failure":
					if name == "shell" {
						args["cmd"] = args["cmd"].(string) + "; exit 3"
					} else {
						args["code"] = args["code"].(string) + "\nraise SystemExit(3)"
					}
				case "timeout":
					if name == "shell" {
						args["timeout_ms"] = 1500
					} else {
						args["timeout"] = 2
					}
				}
				raw, _ := json.Marshal(args)
				deps := isolatedModuleDeps(t)
				client := &asyncProbeChat{contractChat: contractChat{call: llm.ToolCallRequest{Name: name, Arguments: string(raw)}}, sent: make(chan chatcompletions.Request, 5)}
				contractModels(t, &deps, client)
				m := contractSupervisor(t, deps)
				ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 6*time.Second)
				defer cancel()
				send := func(s string) error { return m.HandleMessage(ctx, MessageRequest{Text: s}) }
				done := make(chan error, 1)
				go func() { done <- send("@tool:" + name + " test") }()
				// The original turn must finish while the process is waiting on the release file.
				for waiting := true; waiting; {
					select {
					case err := <-done:
						if err != nil {
							t.Fatal(err)
						}
						waiting = false
					case <-ctx.Done():
						t.Fatal("no acceptance", ctx.Err())
					case <-time.After(5 * time.Millisecond):
						for _, s := range m.turns.SnapshotAll() {
							if s.Phase == turn.PhaseAwaitRiskConfirm {
								if err := send("/confirm"); err != nil {
									t.Fatal(err)
								}
							}
						}
					}
				}
				<-client.sent
				ack := <-client.sent
				if last := ack.Messages[len(ack.Messages)-1]; last.Role != llm.RoleTool || !strings.Contains(llm.SegmentsTextOnly(last.Segments), "已受理") {
					t.Fatalf("unexpected receipt: %+v", last)
				}
				for {
					if _, err := os.Stat(started); err == nil {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("process did not start")
					case <-time.After(5 * time.Millisecond):
					}
				}
				receipt := llm.SegmentsTextOnly(ack.Messages[len(ack.Messages)-1].Segments)
				id := strings.Split(strings.TrimPrefix(receipt, "已受理异步任务 "), "；")[0]
				active, ok := m.requests.Get(id)
				if !ok {
					t.Fatalf("receipt %q does not name an active request", receipt)
				}
				records, err := deps.Store.ToolCalls().ListBySession(ctx, active.SessionID)
				if err != nil || len(records) != 0 {
					t.Fatalf("receipt counted as execution: %+v %v", records, err)
				}
				if stop == "session" || stop == "single" {
					command := "/stop"
					if stop == "single" {
						command += " " + id
					}
					if err := send(command); err != nil {
						t.Fatal(err)
					}
				}
				if stop != "timeout" {
					if err := os.WriteFile(release, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if stop != "session" {
					select {
					case call := <-client.sent:
						ms := call.Messages
						head, out := ms[len(ms)-2], ms[len(ms)-1]
						want := "finished"
						if stop == "timeout" {
							want = "（timeout）"
						}
						if stop == "failure" {
							want = "（failed）"
						}
						if stop == "single" {
							want = id + " 被用户取消"
						}
						if head.Role != llm.RoleAssistant || len(head.ToolCalls) != 1 || head.ToolCalls[0].ID == "contract-call" || head.ToolCalls[0].ID != out.ToolCallID || out.Role != llm.RoleTool || !strings.Contains(llm.SegmentsTextOnly(out.Segments), want) {
							t.Fatalf("bad result pair: %+v %+v", head, out)
						}
					case <-ctx.Done():
						t.Fatal("idle session did not resume")
					}
				}
				for len(m.requests.List()) != 0 {
					select {
					case <-ctx.Done():
						t.Fatal("job did not retire")
					case <-time.After(5 * time.Millisecond):
					}
				}
				select {
				case <-client.sent:
					t.Fatal("unexpected extra LLM call")
				default:
				}
				records, err = deps.Store.ToolCalls().ListBySession(ctx, active.SessionID)
				if err != nil || len(records) != 1 || records[0].ToolCallID != "contract-call" || records[0].Success != (stop == "complete") {
					t.Fatalf("wrong execution audit: %+v %v", records, err)
				}
				bs, err := os.ReadFile(started)
				if err != nil || string(bs) != "x" {
					t.Fatalf("executions=%q error=%v", bs, err)
				}
			})
		}
	}
}
