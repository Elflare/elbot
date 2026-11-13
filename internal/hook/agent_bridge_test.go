// 验证 Agent 参数与结果适配、逐 handler 登记和外部事实转换。

package hook

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	agenthooks "elbot/internal/agent"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/platform"
)

type bridgeTestPipeline struct {
	run    func(context.Context, share.Event, pipeline.CallOptions) (share.Event, error)
	notify func(context.Context, share.Event, pipeline.CallOptions) error
}

func (p bridgeTestPipeline) Run(ctx context.Context, event share.Event, options pipeline.CallOptions) (share.Event, error) {
	if p.run != nil {
		return p.run(ctx, event, options)
	}
	return event, nil
}

func (p bridgeTestPipeline) Notify(ctx context.Context, event share.Event, options pipeline.CallOptions) error {
	if p.notify != nil {
		return p.notify(ctx, event, options)
	}
	return nil
}

type bridgeTestRuntime struct {
	done chan struct{}
	once sync.Once
}

func (*bridgeTestRuntime) HasLease(share.Event) bool { return false }
func (*bridgeTestRuntime) Cancel(share.Event) bool   { return false }
func (r *bridgeTestRuntime) Close(context.Context) error {
	r.once.Do(func() { close(r.done) })
	return nil
}
func (r *bridgeTestRuntime) Done() <-chan struct{} { return r.done }

type bridgeTestAgent struct {
	points       agenthooks.Points
	requests     []string
	finished     int
	disconnected int
}

func (a *bridgeTestAgent) RegisterHooks(points agenthooks.Points) (func(), error) {
	a.points = points
	return func() { a.disconnected++ }, nil
}
func (a *bridgeTestAgent) StartHookRequest(ctx context.Context, sessionID, name string) (context.Context, func(), error) {
	a.requests = append(a.requests, sessionID+":"+name)
	return ctx, func() { a.finished++ }, nil
}

func newBridgeTestSupervisor(t *testing.T) (*Supervisor, *bridgeTestRuntime) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &bridgeTestRuntime{done: make(chan struct{})}
	m := &Supervisor{
		manager: bridgeTestPipeline{}, runtime: runtime,
		parent: context.Background(), ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return m, runtime
}

func TestAgentBridgePassesFactsAndLeavesHandlerTimingToExecutor(t *testing.T) {
	m, _ := newBridgeTestSupervisor(t)
	agent := &bridgeTestAgent{}
	m.manager = bridgeTestPipeline{run: func(ctx context.Context, event share.Event, options pipeline.CallOptions) (share.Event, error) {
		if options.Woken || !options.ReadOnlyCalls || event.Actor.ID != "source" || event.Session.ID != "session" {
			t.Fatalf("lost source facts: event=%+v options=%+v", event, options)
		}
		if len(agent.requests) != 0 {
			t.Fatal("bridge registered a request before an executor selected a handler")
		}
		for _, name := range []string{"first", "second"} {
			_, done, _ := options.Observe(ctx, name)
			done()
		}
		event.Tool.Arguments = `{"edited":true}`
		return event, nil
	}}
	if err := m.ConnectAgent(agent); err != nil {
		t.Fatal(err)
	}
	if err := m.ConnectAgent(&bridgeTestAgent{}); err == nil {
		t.Fatal("duplicate connection accepted")
	}
	got, err := agent.points.ToolCallPrepared(t.Context(), agenthooks.Facts{
		Session: agenthooks.Session{ID: "session"}, Actor: contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: "cli", ID: "source"}}, ReadOnlyCalls: true,
	}, llm.ToolCallRequest{ID: "call", Name: "tool", Arguments: `{}`})
	if err != nil || got != `{"edited":true}` || !reflect.DeepEqual(agent.requests, []string{"session:first", "session:second"}) || agent.finished != 2 {
		t.Fatalf("got=%s requests=%v finished=%d err=%v", got, agent.requests, agent.finished, err)
	}
}

func TestAgentBridgeReturnsPipelineConsumptionAndOutputs(t *testing.T) {
	m, _ := newBridgeTestSupervisor(t)
	m.manager = bridgeTestPipeline{run: func(_ context.Context, event share.Event, _ pipeline.CallOptions) (share.Event, error) {
		event.Control.Consume = true
		event.Message.Segments = llm.TextSegments("processed")
		event.Outputs = []delivery.Output{delivery.Text("extra")}
		return event, nil
	}}
	agent := &bridgeTestAgent{}
	if err := m.ConnectAgent(agent); err != nil {
		t.Fatal(err)
	}
	got, err := agent.points.PlatformMessageReceived(t.Context(), agenthooks.Facts{}, agenthooks.Message{})
	if err != nil || !got.Consume || llm.SegmentsTextOnly(got.Segments) != "processed" || len(got.Outputs) != 1 {
		t.Fatalf("result=%+v err=%v", got, err)
	}
}

func TestAgentBridgeToolResultTextPreservesMediaAndSegmentEditsWin(t *testing.T) {
	for _, editSegments := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "segments"}[editSegments], func(t *testing.T) {
			m, _ := newBridgeTestSupervisor(t)
			m.manager = bridgeTestPipeline{run: func(_ context.Context, event share.Event, _ pipeline.CallOptions) (share.Event, error) {
				event.Tool.Result.Data = "text edit"
				if editSegments {
					event.Message.Segments[0].Text = "segment edit"
				}
				return event, nil
			}}
			agent := &bridgeTestAgent{}
			if err := m.ConnectAgent(agent); err != nil {
				t.Fatal(err)
			}
			original := []llm.MessageSegment{{Type: llm.SegmentText, Text: "original"}, {Type: llm.SegmentImage, MediaID: "image-id"}}
			got, err := agent.points.ToolCallCompleted(t.Context(), agenthooks.Facts{}, agenthooks.ToolResult{Result: operationresult.Result[[]llm.MessageSegment]{Status: operationresult.Success, Data: original}})
			want := "text edit"
			if editSegments {
				want = "segment edit"
			}
			if err != nil || llm.SegmentsTextOnly(got) != want || len(got) != 2 || got[1] != original[1] || original[0].Text != "original" {
				t.Fatalf("segments=%+v original=%+v err=%v", got, original, err)
			}
		})
	}
}

func TestAgentBridgeReturnsPipelineErrorWithoutNotifyingAgain(t *testing.T) {
	m, _ := newBridgeTestSupervisor(t)
	original := errors.New("handler failed")
	var notified []share.Point
	m.manager = bridgeTestPipeline{
		run: func(_ context.Context, event share.Event, _ pipeline.CallOptions) (share.Event, error) {
			return event, original
		},
		notify: func(_ context.Context, event share.Event, _ pipeline.CallOptions) error {
			notified = append(notified, event.Point)
			if event.Error != original {
				t.Fatal("lost original error")
			}
			return errors.New("error handler failed")
		},
	}
	agent := &bridgeTestAgent{}
	if err := m.ConnectAgent(agent); err != nil {
		t.Fatal(err)
	}
	_, err := agent.points.AgentInputPrepared(t.Context(), agenthooks.Facts{}, agenthooks.Message{})
	if err != original || len(notified) != 0 {
		t.Fatal("notified= result= err=", notified, err)
	}
}

func TestHookContextProjectsReplyWithoutChangingOriginalPlatformData(t *testing.T) {
	raw := []byte(`{"original":true}`)
	ctx := platform.WithMessageContext(t.Context(), platform.MessageContext{
		RawText: "wake original", PlatformMessage: raw,
		Reply: platform.ReplyContext{MessageID: "reply", SenderID: "sender", Segments: []platform.MessageSegment{
			{Type: platform.SegmentText, Text: "quoted"},
			{Type: platform.SegmentImage, MediaID: "quoted-image"},
		}},
	})
	event := fillEventFromContext(ctx, share.Event{Point: share.PointPlatformMessageReceived, Message: share.MessagePayload{IntentText: "prepared intent"}})
	if event.Message.PlatformText != "wake original" || event.Message.IntentText != "prepared intent" || event.Message.Reply == nil || len(event.Message.Reply.Segments) != 2 || event.Message.Reply.Segments[1].MediaID != "quoted-image" {
		t.Fatalf("message=%+v", event.Message)
	}
	event.Message.PlatformMessage[0] = '!'
	if raw[0] != '{' || event.Actor.Identity() != (contextinfo.ActorIdentity{}) {
		t.Fatal("shared platform payload mutated or local identity invented")
	}
}
