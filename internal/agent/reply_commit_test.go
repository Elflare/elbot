package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type replyTestRepository struct {
	storage.MessageRepository
	events            *[]string
	appendErr, mapErr error
}

func (r *replyTestRepository) Append(ctx context.Context, msg *storage.Message) error {
	*r.events = append(*r.events, "save")
	if r.appendErr != nil && msg.Role == storage.RoleAssistant {
		return r.appendErr
	}
	return r.MessageRepository.Append(ctx, msg)
}

func (r *replyTestRepository) MapPlatformMessage(ctx context.Context, m storage.PlatformMessageMap) error {
	*r.events = append(*r.events, "map")
	if r.mapErr != nil {
		return r.mapErr
	}
	return r.MessageRepository.MapPlatformMessage(ctx, m)
}

type replyTestPlatform struct {
	fakePlatform
	events              *[]string
	receipt             delivery.Receipt
	sendErr, outputsErr error
	text                string
	texts               []string
}

func (p *replyTestPlatform) SendChat(_ context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	*p.events = append(*p.events, "send")
	p.text = delivery.FallbackOutput(outputs).Text
	p.texts = append(p.texts, p.text)
	return p.receipt, p.sendErr
}

func (p *replyTestPlatform) SendNotice(_ context.Context, notice delivery.Notice) (delivery.Receipt, error) {
	*p.events = append(*p.events, "outputs")
	return delivery.Receipt{}, p.outputsErr
}

type replyTestStream struct {
	events                        *[]string
	replaceReceipt, finishReceipt delivery.Receipt
	replaceErr, finishErr         error
	text                          string
	wantCtx                       context.Context
	t                             *testing.T
}

func (s *replyTestStream) Append(context.Context, string) error { return nil }
func (s *replyTestStream) Replace(ctx context.Context, text string) (delivery.Receipt, error) {
	if ctx != s.wantCtx {
		s.t.Fatal("stream lost request context")
	}
	*s.events = append(*s.events, "replace")
	s.text = text
	return s.replaceReceipt, s.replaceErr
}
func (s *replyTestStream) Finish(ctx context.Context) (delivery.Receipt, error) {
	if ctx != s.wantCtx {
		s.t.Fatal("stream lost request context")
	}
	*s.events = append(*s.events, "finish")
	return s.finishReceipt, s.finishErr
}

// The fixture uses the real output adapters and SQLite without constructing Agent.
type replyTestFixture struct {
	events          []string
	ctx, requestCtx context.Context
	repo            *replyTestRepository
	platform        *replyTestPlatform
	committer       *replyCommitter
	out             turnOutput
	in              replyCommitInput
	hooks           *hook.DefaultManager
}

func newReplyTestFixture(t *testing.T, buffered bool) *replyTestFixture {
	t.Helper()
	f := &replyTestFixture{}
	store := newTestStore(t)
	row := &storage.Session{OwnerID: "owner", Platform: "cli", PlatformScopeID: "local", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive}
	if err := store.Sessions().Create(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	f.repo = &replyTestRepository{MessageRepository: store.Messages(), events: &f.events}
	f.platform = &replyTestPlatform{events: &f.events, receipt: delivery.Receipt{
		PlatformMessageIDs: []string{"sent"}, SentMessages: []delivery.SentMessage{{Platform: "telegram", ScopeID: "private:actual", PlatformMessageID: "sent"}},
	}}
	f.ctx = platform.WithMessageContext(context.Background(), platform.MessageContext{BufferAssistantOutput: buffered})
	var cancel context.CancelFunc
	f.requestCtx, cancel = context.WithCancel(f.ctx)
	t.Cleanup(cancel)
	f.hooks = hook.NewManager()
	sender := &outputSender{dispatcher: dispatch.New(dispatch.Options{Primary: f.platform}), hooks: &hookBridge{manager: f.hooks, identity: &identityResolver{platformName: "cli"}}}
	f.committer = &replyCommitter{messages: f.repo, output: sender}
	f.out = foregroundTurnOutput{sender: sender, status: &statusRecorder{}}
	f.in = replyCommitInput{Session: row, Text: "history", RawText: "raw", PlatformText: "visible", Outputs: []delivery.Output{delivery.Text("later")}}
	return f
}

func (f *replyTestFixture) captureHooks(t *testing.T) {
	t.Helper()
	for _, point := range []hook.Point{hook.PointAgentTurnOutputPrepared, hook.PointAgentOutputPrepared} {
		if err := f.hooks.Register(hook.Registration{Point: point, Name: string(point), Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, error) {
			suffix := "/send"
			if point == hook.PointAgentTurnOutputPrepared {
				suffix = "/turn"
			}
			f.events = append(f.events, suffix)
			e.Message.Segments = llm.AppendSegmentText(e.Message.Segments, suffix)
			return e, nil
		})}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReplyCommitOrderAndTextSeparation(t *testing.T) {
	for _, mode := range []string{"direct", "buffered", "stream"} {
		t.Run(mode, func(t *testing.T) {
			f := newReplyTestFixture(t, mode == "buffered")
			f.captureHooks(t)
			var stream *replyTestStream
			wantOrder := []string{"/turn", "/send", "send", "outputs", "save", "map"}
			if mode == "buffered" {
				wantOrder = []string{"/turn", "save", "/send", "send", "map", "outputs"}
			}
			if mode == "stream" {
				stream = &replyTestStream{events: &f.events, replaceReceipt: f.platform.receipt, wantCtx: f.requestCtx, t: t}
				f.in.Stream = stream
				wantOrder = []string{"/turn", "/send", "replace", "finish", "outputs", "save", "map"}
			}
			got, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
			if err != nil || !got.Persisted || got.MessageID == "" || got.RawText != "raw" {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			if !reflect.DeepEqual(f.events, wantOrder) {
				t.Fatalf("order=%v want=%v", f.events, wantOrder)
			}
			text := f.platform.text
			if stream != nil {
				text = stream.text
			}
			if text != "visible/turn/send" {
				t.Fatalf("display=%q", text)
			}
			message, err := f.repo.Get(f.ctx, got.MessageID)
			if err != nil || message.Content != "history" || message.Metadata != assistantRawTextMetadata("history", "raw") {
				t.Fatalf("history=%+v err=%v", message, err)
			}
			mapped, err := f.repo.FindByPlatformMessage(f.ctx, "telegram", "private:actual", "sent")
			if err != nil || mapped.ID != got.MessageID {
				t.Fatalf("mapping=%+v err=%v", mapped, err)
			}
		})
	}
}

func TestReplyCommitFailureKeepsFactsAndDoesNotRetry(t *testing.T) {
	sendErr, saveErr, mapErr := errors.New("send failed"), errors.New("save failed"), errors.New("map failed")
	for _, buffered := range []bool{false, true} {
		for _, failure := range []string{"send", "partial", "save", "partial_and_save", "outputs", "map"} {
			t.Run(map[bool]string{false: "direct", true: "buffered"}[buffered]+"/"+failure, func(t *testing.T) {
				f := newReplyTestFixture(t, buffered)
				wantErr := sendErr
				wantPersisted, wantReceipt := true, true
				switch failure {
				case "send":
					f.platform.sendErr = sendErr
					f.platform.receipt = delivery.Receipt{}
					wantReceipt = false
					wantPersisted = buffered
				case "partial":
					f.platform.sendErr = sendErr
				case "save":
					f.repo.appendErr = saveErr
					wantErr = saveErr
					wantPersisted = false
					wantReceipt = !buffered
				case "partial_and_save":
					f.platform.sendErr = sendErr
					f.repo.appendErr = saveErr
					wantErr = saveErr
					wantPersisted = false
					wantReceipt = !buffered
				case "outputs":
					f.platform.outputsErr = sendErr
				case "map":
					f.repo.mapErr = mapErr
					wantErr = nil
				}
				got, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
				if !errors.Is(err, wantErr) || got.Persisted != wantPersisted || hasReplyReceipt(got.Receipt) != wantReceipt {
					t.Fatalf("result=%+v err=%v events=%v", got, err, f.events)
				}
				if !errors.Is(got.PersistErr, f.repo.appendErr) {
					t.Fatalf("lost save failure: %+v", got)
				}
				if failure == "partial_and_save" && !buffered && !errors.Is(got.SendErr, sendErr) {
					t.Fatalf("lost earlier send failure: %+v", got)
				}
				if failure == "map" && (len(got.AssociationErrors) != 1 || !errors.Is(got.AssociationErrors[0], mapErr)) {
					t.Fatalf("lost association failure: %+v", got)
				}
				sends, maps := 0, 0
				for _, event := range f.events {
					if event == "send" {
						sends++
					}
					if event == "map" {
						maps++
					}
				}
				wantSends := 1
				if buffered && f.repo.appendErr != nil {
					wantSends = 0
				}
				if sends != wantSends || (maps > 0) != (wantPersisted && wantReceipt) {
					t.Fatalf("unexpected retry/association: %v", f.events)
				}
				if wantPersisted && wantReceipt && failure != "map" {
					mapped, err := f.repo.FindByPlatformMessage(f.ctx, "telegram", "private:actual", "sent")
					if err != nil || mapped.ID != got.MessageID {
						t.Fatalf("lost successful receipt mapping: %+v %v", mapped, err)
					}
				}
			})
		}
	}
}

func TestReplyCommitStreamFailureRetainsReceipt(t *testing.T) {
	failure := errors.New("stream failed")
	for _, mode := range []string{"replace", "partial_replace", "finish", "partial_finish"} {
		t.Run(mode, func(t *testing.T) {
			f := newReplyTestFixture(t, false)
			s := &replyTestStream{events: &f.events, wantCtx: f.requestCtx, t: t}
			f.in.Stream = s
			switch mode {
			case "replace":
				s.replaceErr = failure
			case "partial_replace":
				s.replaceReceipt = f.platform.receipt
				s.replaceErr = failure
			case "finish":
				s.replaceReceipt = f.platform.receipt
				s.finishErr = failure
			case "partial_finish":
				s.finishReceipt = f.platform.receipt
				s.finishErr = failure
			}
			got, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
			if !errors.Is(err, failure) || got.Persisted != (mode != "replace") || hasReplyReceipt(got.Receipt) != (mode != "replace") {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			if strings.Contains(strings.Join(f.events, ","), "send") || strings.Contains(strings.Join(f.events, ","), "outputs") {
				t.Fatalf("stream retried or continued after failure: %v", f.events)
			}
		})
	}
}

func TestReplyCommitEmptyAndBackground(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		for _, mode := range []string{"empty", "background_empty", "background_reply", "outputs_only"} {
			t.Run(map[bool]string{false: "direct", true: "buffered"}[buffered]+"/"+mode, func(t *testing.T) {
				f := newReplyTestFixture(t, buffered)
				f.in.Text, f.in.RawText, f.in.PlatformText, f.in.Outputs = "", "", "", nil
				if strings.HasPrefix(mode, "background") {
					f.in.Session.Mode = storage.SessionModeBackground
					f.out = backgroundTurnOutput{status: &statusRecorder{}}
				}
				if mode == "background_reply" {
					f.in.Text, f.in.RawText, f.in.PlatformText = "raw", "raw", "visible"
				}
				if mode == "outputs_only" {
					f.in.Outputs = []delivery.Output{delivery.Text("attachment")}
				}
				got, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
				if err != nil || got.Persisted != (mode == "outputs_only" || mode == "background_reply") {
					t.Fatalf("result=%+v err=%v", got, err)
				}
				if mode == "empty" && f.platform.text != "模型这次没有返回可见内容。" {
					t.Fatalf("fallback=%q", f.platform.text)
				}
				if mode != "empty" && f.platform.text != "" {
					t.Fatalf("unexpected assistant output=%q", f.platform.text)
				}
			})
		}
	}
}

func TestReplyCommitFinalHookFailureDoesNotSaveOrSend(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "buffered"}[buffered], func(t *testing.T) {
			f := newReplyTestFixture(t, buffered)
			if err := f.hooks.Register(hook.Registration{Point: hook.PointAgentTurnOutputPrepared, Name: "cancel", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, error) { return e, context.Canceled })}); err != nil {
				t.Fatal(err)
			}
			got, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
			if !errors.Is(err, context.Canceled) || got.Persisted || hasReplyReceipt(got.Receipt) || len(f.events) != 0 {
				t.Fatalf("result=%+v err=%v events=%v", got, err, f.events)
			}
		})
	}
}

func TestReplyCommitHookSuppressesDisplayWithoutChangingHistory(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "buffered"}[buffered], func(t *testing.T) {
			f := newReplyTestFixture(t, buffered)
			for _, point := range []hook.Point{hook.PointAgentTurnOutputPrepared, hook.PointAgentOutputPrepared} {
				if err := f.hooks.Register(hook.Registration{Point: point, Name: string(point), Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, error) {
					f.events = append(f.events, string(point))
					e.Message.Segments = nil
					return e, nil
				})}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
			if err != nil || !got.Persisted || f.platform.text != "" {
				t.Fatalf("result=%+v err=%v text=%q", got, err, f.platform.text)
			}
			message, err := f.repo.Get(f.ctx, got.MessageID)
			if err != nil || message.Content != "history" {
				t.Fatalf("history=%+v err=%v", message, err)
			}
			count := 0
			for _, event := range f.events {
				if event == string(hook.PointAgentOutputPrepared) {
					count++
				}
			}
			want := 1
			if buffered {
				want = 0
			}
			if count != want {
				t.Fatalf("send hook calls=%d want=%d", count, want)
			}
		})
	}
}

type replyTestStore struct {
	storage.Store
	repo storage.MessageRepository
}

func (s replyTestStore) Messages() storage.MessageRepository { return s.repo }

func TestChatReplySaveFailureKeepsExecutionFailed(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "buffered"}[buffered], func(t *testing.T) {
			base := newTestStore(t)
			var events []string
			failure := errors.New("assistant save failed")
			repo := &replyTestRepository{MessageRepository: base.Messages(), events: &events, appendErr: failure}
			p := &replyTestPlatform{events: &events}
			model := &fakeLLM{replies: []string{"visible answer"}, titleReplies: []string{"should not be used"}}
			a := newTestAgent(t, p, model, "model", config.ProviderConfig{}, replyTestStore{Store: base, repo: repo})
			execution := turn.NewExecution("reply-save-failure")
			ctx := turn.WithExecution(platform.WithMessageContext(context.Background(), platform.MessageContext{BufferAssistantOutput: buffered}), execution)
			if err := a.HandleMessage(ctx, "question"); !errors.Is(err, failure) {
				t.Fatalf("chat error=%v", err)
			}
			waitCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			result := execution.Wait(waitCtx)
			if !errors.Is(result.Err, failure) || result.Outcome != "failed" || result.MessageID != "" || result.Text != "" {
				t.Fatalf("execution=%+v", result)
			}
			row, err := a.execution.sessions.Current(ctx, a.identity.Scope(ctx))
			if err != nil {
				t.Fatal(err)
			}
			if a.RuntimeStatus(row.ID).Phase != runtimestatus.PhaseError || a.execution.turns.Snapshot(row.ID).Phase != turn.PhaseIdle || len(a.execution.requests.List()) != 0 {
				t.Fatal("failed reply retained execution or published success")
			}
			messages, err := base.Messages().ListBySession(ctx, row.ID)
			if err != nil || len(messages) != 1 || messages[0].Role != storage.RoleUser {
				t.Fatalf("history=%+v err=%v", messages, err)
			}
			answers := 0
			for _, text := range p.texts {
				if text == "visible answer" {
					answers++
				}
			}
			wantAnswers := 1
			if buffered {
				wantAnswers = 0
			}
			if answers != wantAnswers {
				t.Fatalf("assistant sends=%d want=%d texts=%q", answers, wantAnswers, p.texts)
			}
			if err := a.execution.sessions.Close(waitCtx); err != nil {
				t.Fatal(err)
			}
			if model.requestCount() != 1 {
				t.Fatalf("unexpected follow-up or naming: %d", model.requestCount())
			}
		})
	}
}
