package execution

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/agent/dialogue"
	agenthooks "elbot/internal/agent/hooks"
	agentoutput "elbot/internal/agent/output"
	agentshare "elbot/internal/agent/share"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/platform/dispatch"

	"elbot/internal/security"
	"elbot/internal/session"
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

type replyTestPlatform struct {
	fakePlatform
	events                          *[]string
	receipt                         delivery.Receipt
	outputsReceipt                  delivery.Receipt
	sendErr, outputsErr             error
	sendLogResult, outputsLogResult *events.LogResult
	text                            string
	texts                           []string
}

func (p *replyTestPlatform) SendChat(_ context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	*p.events = append(*p.events, "send")
	p.text = delivery.FallbackOutput(outputs).Text
	p.texts = append(p.texts, p.text)
	{
		err := p.sendErr
		logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
		if err != nil {
			logResult.ResultStatus = events.ResultFailed
			logResult.Content = err.Error()
			if err == context.Canceled {
				logResult.ResultStatus = events.ResultCanceled
			}
		}
		if p.sendLogResult != nil {
			logResult = *p.sendLogResult
		}
		return p.receipt, logResult, err
	}
}

func (p *replyTestPlatform) SendTo(_ context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	*p.events = append(*p.events, "outputs")
	{
		err := p.outputsErr
		logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
		if err != nil {
			logResult.ResultStatus = events.ResultFailed
			logResult.Content = err.Error()
			if err == context.Canceled {
				logResult.ResultStatus = events.ResultCanceled
			}
		}
		if p.outputsLogResult != nil {
			logResult = *p.outputsLogResult
		}
		return p.outputsReceipt, logResult, err
	}
}

type replyTestStream struct {
	events                            *[]string
	replaceReceipt, finishReceipt     delivery.Receipt
	replaceErr, finishErr             error
	replaceLogResult, finishLogResult *events.LogResult
	text                              string
	wantCtx                           context.Context
	t                                 *testing.T
}

func (s *replyTestStream) Append(context.Context, string) error { return nil }

func (s *replyTestStream) Replace(ctx context.Context, text string) (delivery.Receipt, events.LogResult, error) {
	if ctx != s.wantCtx {
		s.t.Fatal("stream lost request context")
	}
	*s.events = append(*s.events, "replace")
	s.text = text
	{
		err := s.replaceErr
		logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
		if err != nil {
			logResult.ResultStatus = events.ResultFailed
			logResult.Content = err.Error()
			if err == context.Canceled {
				logResult.ResultStatus = events.ResultCanceled
			}
		}
		if s.replaceLogResult != nil {
			logResult = *s.replaceLogResult
		}
		return s.replaceReceipt, logResult, err
	}
}

func (s *replyTestStream) Finish(ctx context.Context) (delivery.Receipt, events.LogResult, error) {
	if ctx != s.wantCtx {
		s.t.Fatal("stream lost request context")
	}
	*s.events = append(*s.events, "finish")
	{
		err := s.finishErr
		logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
		if err != nil {
			logResult.ResultStatus = events.ResultFailed
			logResult.Content = err.Error()
			if err == context.Canceled {
				logResult.ResultStatus = events.ResultCanceled
			}
		}
		if s.finishLogResult != nil {
			logResult = *s.finishLogResult
		}
		return s.finishReceipt, logResult, err
	}
}

// The fixture uses real output adapters and SQLite for isolated reply commit tests.
type replyTestFixture struct {
	events          []string
	ctx, requestCtx context.Context
	repo            *replyTestRepository
	platform        *replyTestPlatform
	committer       *dialogue.ReplyCommitter
	out             agentshare.Output
	in              agentshare.ReplyCommitInput
	hooks           *agenthooks.HookBridge
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
	f.hooks = agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{Identity: agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{PlatformName: "cli", Permission: security.DefaultPermission()})})
	t.Cleanup(func() { _ = f.hooks.Close(context.Background()) })

	sender := agentoutput.NewOutputSender(agentoutput.OutputSenderDeps{
		Dispatcher: dispatch.New(dispatch.Options{Primary: f.platform}),
		Hooks:      f.hooks,
	})
	messages := &dialogue.MessageStore{Dialogues: replyTestDialogues{DialogueRepository: store.Dialogues(), repo: f.repo}, Gate: &dialogue.CommitGate{Sessions: session.NewService(store), Turns: turn.NewManager(), View: dialogue.ExecutionView{Sessions: store.Sessions()}}}
	f.committer = &dialogue.ReplyCommitter{Persistence: messages.Committer("append_assistant_message"), Output: sender}
	f.out = foregroundTurnOutput{sender: sender}
	f.in = agentshare.ReplyCommitInput{Session: row, Text: "history", RawText: "raw", PlatformText: "visible", Outputs: []delivery.Output{delivery.Text("later")}}
	return f
}

func (f *replyTestFixture) register(t *testing.T, points agenthooks.Points) {
	t.Helper()
	if _, err := f.hooks.Register(points); err != nil {
		t.Fatal(err)
	}
}

func (f *replyTestFixture) captureHooks(t *testing.T) {
	t.Helper()
	appendText := func(suffix string) func(context.Context, agenthooks.Facts, string) (string, events.LogResult, error) {
		return func(_ context.Context, _ agenthooks.Facts, text string) (string, events.LogResult, error) {
			f.events = append(f.events, suffix)
			return text + suffix, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
		}
	}
	f.register(t, agenthooks.Points{AgentTurnOutputPrepared: appendText("/turn"), AgentOutputPrepared: appendText("/send")})
}

func TestReplyCommitOrderAndTextSeparation(t *testing.T) {
	for _, mode := range []string{"direct", "buffered", "stream"} {
		t.Run(mode, func(t *testing.T) {
			f := newReplyTestFixture(t, mode == "buffered")
			f.captureHooks(t)
			var stream *replyTestStream
			wantOrder := []string{"/turn", "/send", "send", "save", "map", "outputs"}
			if mode == "stream" {
				stream = &replyTestStream{events: &f.events, replaceReceipt: f.platform.receipt, wantCtx: f.requestCtx, t: t}
				f.in.Stream = stream
				wantOrder = []string{"/turn", "/send", "replace", "finish", "save", "map", "outputs"}
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
			if err != nil || message.Content != "history" || message.Metadata != dialogue.AssistantRawTextMetadata("history", "raw") {
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
					wantPersisted = false
				case "partial":
					f.platform.sendErr = sendErr
				case "save":
					f.repo.appendErr = saveErr
					wantErr = saveErr
					wantPersisted = false
					wantReceipt = true
				case "partial_and_save":
					f.platform.sendErr = sendErr
					f.repo.appendErr = saveErr
					wantErr = saveErr
					wantPersisted = false
					wantReceipt = true
				case "outputs":
					f.platform.outputsErr = sendErr
				case "map":
					f.repo.mapErr = mapErr
					wantErr = mapErr
					wantPersisted = false
				}
				got, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
				if !errors.Is(err, wantErr) || got.Persisted != wantPersisted || dialogue.HasReplyReceipt(got.Receipt) != wantReceipt {
					t.Fatalf("result=%+v err=%v events=%v", got, err, f.events)
				}
				if failure != "map" && !errors.Is(got.PersistErr, f.repo.appendErr) {
					t.Fatalf("lost save failure: %+v", got)
				}
				if failure == "partial_and_save" && !buffered && !errors.Is(got.SendErr, sendErr) {
					t.Fatalf("lost earlier send failure: %+v", got)
				}
				if failure == "map" && !errors.Is(got.PersistErr, mapErr) {
					t.Fatalf("lost association failure: %+v", got)
				}
				if failure == "map" {
					rows, err := f.repo.ListBySession(f.ctx, f.in.Session.ID)
					if err != nil || len(rows) != 0 {
						t.Fatalf("failed transaction retained history: %+v %v", rows, err)
					}
					if _, err := f.repo.FindByPlatformMessage(f.ctx, "telegram", "private:actual", "sent"); !errors.Is(err, storage.ErrNotFound) {
						t.Fatalf("failed transaction retained mapping: %v", err)
					}
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
				if sends != wantSends || (maps > 0) != (wantReceipt && f.repo.appendErr == nil) {
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
			if !errors.Is(err, failure) || got.Persisted != (mode != "replace") || dialogue.HasReplyReceipt(got.Receipt) != (mode != "replace") {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			if strings.Contains(strings.Join(f.events, ","), "send") || strings.Contains(strings.Join(f.events, ","), "outputs") {
				t.Fatalf("stream retried or continued after failure: %v", f.events)
			}
		})
	}
}

func TestReplyCommitMergesReplaceAndFinishReceipts(t *testing.T) {
	f := newReplyTestFixture(t, false)
	s := &replyTestStream{events: &f.events, wantCtx: f.requestCtx, t: t,
		replaceReceipt: delivery.Receipt{
			PlatformMessageIDs: []string{"first"},
			SentMessages:       []delivery.SentMessage{{Platform: "telegram", ScopeID: "scope", PlatformMessageID: "first", OutputIndexes: []int{0}}},
		},
		finishReceipt: delivery.Receipt{
			PlatformMessageIDs: []string{"first", "second"},
			SentMessages:       []delivery.SentMessage{{Platform: "telegram", ScopeID: "scope", PlatformMessageID: "first", OutputIndexes: []int{0, 1}}, {Platform: "telegram", ScopeID: "scope", PlatformMessageID: "second", OutputIndexes: []int{2}}},
		},
		finishErr: errors.New("finish partially failed"),
	}
	f.in.Stream = s
	got, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
	if !errors.Is(err, s.finishErr) || !got.Persisted || !reflect.DeepEqual(got.Receipt.PlatformMessageIDs, []string{"first", "second"}) || len(got.Receipt.SentMessages) != 2 || !reflect.DeepEqual(got.Receipt.SentMessages[0].OutputIndexes, []int{0, 1}) {
		t.Fatalf("result=%+v error=%v", got, err)
	}
	got.Receipt.SentMessages[0].OutputIndexes[0] = 9
	if s.replaceReceipt.SentMessages[0].OutputIndexes[0] != 0 || s.finishReceipt.SentMessages[0].OutputIndexes[0] != 0 {
		t.Fatal("merged receipt aliases platform receipt")
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
					f.out = backgroundTurnOutput{}
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
			f.register(t, agenthooks.Points{AgentTurnOutputPrepared: func(_ context.Context, _ agenthooks.Facts, text string) (string, events.LogResult, error) {
				return text, events.LogResult{ResultStatus: events.ResultCanceled}, context.Canceled
			}})
			got, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
			if !errors.Is(err, context.Canceled) || got.Persisted || dialogue.HasReplyReceipt(got.Receipt) || len(f.events) != 0 {
				t.Fatalf("result=%+v err=%v events=%v", got, err, f.events)
			}
		})
	}
}

func TestReplyCommitHookSuppressesDisplayWithoutChangingHistory(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "buffered"}[buffered], func(t *testing.T) {
			f := newReplyTestFixture(t, buffered)
			suppress := func(point string) func(context.Context, agenthooks.Facts, string) (string, events.LogResult, error) {
				return func(_ context.Context, _ agenthooks.Facts, _ string) (string, events.LogResult, error) {
					f.events = append(f.events, point)
					return "", events.LogResult{ResultStatus: events.ResultSucceeded}, nil
				}
			}
			f.register(t, agenthooks.Points{AgentTurnOutputPrepared: suppress("turn"), AgentOutputPrepared: suppress("send_hook")})
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
				if event == "send_hook" {
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

type replyTestDialogues struct {
	storage.DialogueRepository
	repo *replyTestRepository
}

func (r replyTestDialogues) Commit(ctx context.Context, commit storage.DialogueCommit) (events.LogResult, error) {
	for _, msg := range commit.Messages {
		*r.repo.events = append(*r.repo.events, "save")
		if msg.Role == storage.RoleAssistant && r.repo.appendErr != nil {
			return events.LogResult{ResultStatus: events.ResultFailed}, r.repo.appendErr
		}
	}
	for range commit.PlatformMessageMaps {
		*r.repo.events = append(*r.repo.events, "map")
	}
	if r.repo.mapErr != nil && len(commit.PlatformMessageMaps) > 0 {
		commit.PlatformMessageMaps = append(commit.PlatformMessageMaps, commit.PlatformMessageMaps[0])
	}
	logResult, err := r.DialogueRepository.Commit(ctx, commit)
	if err != nil && r.repo.mapErr != nil {
		err = errors.Join(err, r.repo.mapErr)
	}
	return logResult, err
}
