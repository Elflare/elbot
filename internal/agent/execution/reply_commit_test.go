package execution

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/testutil"
	"errors"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/agent/dialogue"
	agenthooks "elbot/internal/agent/hooks"
	agentoutput "elbot/internal/agent/output"
	agentshare "elbot/internal/agent/share"

	"elbot/internal/delivery"
	"elbot/internal/platform/dispatch"

	"elbot/internal/security"
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
	events              *[]string
	receipt             delivery.Receipt
	outputsReceipt      delivery.Receipt
	expectedOutputs     []delivery.Output
	sendErr, outputsErr error

	text  string
	texts []string
}

func (p *replyTestPlatform) SendTo(_ context.Context, _ delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	receipt, err := p.receipt, p.sendErr
	if reflect.DeepEqual(outputs, p.expectedOutputs) {
		*p.events = append(*p.events, "outputs")
		receipt, err = p.outputsReceipt, p.outputsErr
	} else {
		*p.events = append(*p.events, "send")
		p.text = delivery.FallbackOutput(outputs).Text
		p.texts = append(p.texts, p.text)
	}
	return receipt, err
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
	row := &storage.Session{ID: storage.NewID(), Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "owner"}, Platform: "cli", ChatID: "local", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, ChatType: "private"}
	if err := store.Sessions().Create(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	f.repo = &replyTestRepository{MessageRepository: store.Messages(), events: &f.events}
	f.platform = &replyTestPlatform{events: &f.events, receipt: delivery.Receipt{
		PlatformMessageIDs: []string{"sent"}, SentMessages: []delivery.SentMessage{{Platform: "telegram", ChatType: "private", ChatID: "actual", PlatformMessageID: "sent"}},
	}}
	f.ctx = agentshare.WithMessageContext(context.Background(), agentshare.MessageContext{BufferAssistantOutput: buffered})
	var cancel context.CancelFunc
	f.requestCtx, cancel = context.WithCancel(f.ctx)
	t.Cleanup(cancel)
	f.hooks = agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{Identity: agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})})
	t.Cleanup(func() { _ = f.hooks.Close(context.Background()) })

	sender := agentoutput.NewOutputSender(agentoutput.OutputSenderDeps{
		Dispatcher: dispatch.New(dispatch.Options{Primary: f.platform}),
		Hooks:      f.hooks,
	})
	messages := &dialogue.MessageStore{NewMessageID: testutil.IDSequence().MessageID, Dialogues: replyTestDialogues{DialogueRepository: store.Dialogues(), repo: f.repo}, Gate: &dialogue.CommitGate{Sessions: newSessionSupervisor(t, store), Turns: turn.NewSupervisor(testutil.IDSequence()), View: dialogue.ExecutionView{Sessions: store.Sessions()}}}
	f.committer = &dialogue.ReplyCommitter{NewMessageID: testutil.IDSequence().MessageID, Persistence: messages.Committer("append_assistant_message"), Output: sender}
	f.out = foregroundTurnOutput{ExecutionSender: sender}
	f.in = agentshare.ReplyCommitInput{Session: row, Text: "history", RawText: "raw", PlatformText: "visible", Outputs: []delivery.Output{delivery.Text("later")}}
	f.in.Stream = f.out.StartStream(f.requestCtx)
	f.platform.expectedOutputs = f.in.Outputs
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
	appendText := func(suffix string) func(context.Context, agenthooks.Facts, string) (string, error) {
		return func(_ context.Context, _ agenthooks.Facts, text string) (string, error) {
			f.events = append(f.events, suffix)
			return text + suffix, nil
		}
	}
	f.register(t, agenthooks.Points{AgentTurnOutputPrepared: appendText("/turn"), AgentOutputPrepared: appendText("/send")})
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
				if !errors.Is(err, wantErr) || (got.MessageID != "") != wantPersisted || dialogue.HasReplyReceipt(got.Receipt) != wantReceipt {
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
					if _, err := f.repo.FindByPlatformMessage(f.ctx, "telegram", "private", "actual", "sent"); !errors.Is(err, storage.ErrNotFound) {
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
					mapped, err := f.repo.FindByPlatformMessage(f.ctx, "telegram", "private", "actual", "sent")
					if err != nil || mapped.ID != got.MessageID {
						t.Fatalf("lost successful receipt mapping: %+v %v", mapped, err)
					}
				}
			})
		}
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
					f.platform.expectedOutputs = f.in.Outputs
				}
				got, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
				if err != nil || (got.MessageID != "") != (mode == "outputs_only" || mode == "background_reply") {
					t.Fatalf("result=%+v err=%v", got, err)
				}
				if mode == "empty" && f.platform.text != "LLM: nil" {
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
			f.register(t, agenthooks.Points{AgentTurnOutputPrepared: func(_ context.Context, _ agenthooks.Facts, text string) (string, error) {
				return text, context.Canceled
			}})
			got, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
			if !errors.Is(err, context.Canceled) || (got.MessageID != "") || dialogue.HasReplyReceipt(got.Receipt) || len(f.events) != 0 {
				t.Fatalf("result=%+v err=%v events=%v", got, err, f.events)
			}
		})
	}
}

func TestReplyCommitHookSuppressesDisplayWithoutChangingHistory(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "buffered"}[buffered], func(t *testing.T) {
			f := newReplyTestFixture(t, buffered)
			suppress := func(point string) func(context.Context, agenthooks.Facts, string) (string, error) {
				return func(_ context.Context, _ agenthooks.Facts, _ string) (string, error) {
					f.events = append(f.events, point)
					return "", nil
				}
			}
			f.register(t, agenthooks.Points{AgentTurnOutputPrepared: suppress("turn"), AgentOutputPrepared: suppress("send_hook")})
			got, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
			if err != nil || got.MessageID == "" || f.platform.text != "" {
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

func (r replyTestDialogues) Commit(ctx context.Context, commit storage.DialogueCommit) error {
	for _, msg := range commit.Messages {
		*r.repo.events = append(*r.repo.events, "save")
		if msg.Role == storage.RoleAssistant && r.repo.appendErr != nil {
			return r.repo.appendErr
		}
	}
	for range commit.PlatformMessageMaps {
		*r.repo.events = append(*r.repo.events, "map")
	}
	if r.repo.mapErr != nil && len(commit.PlatformMessageMaps) > 0 {
		commit.PlatformMessageMaps = append(commit.PlatformMessageMaps, commit.PlatformMessageMaps[0])
	}
	err := r.DialogueRepository.Commit(ctx, commit)
	if err != nil && r.repo.mapErr != nil {
		err = errors.Join(err, r.repo.mapErr)
	}
	return err
}
