package agent

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	hookbuiltin "elbot/internal/hook/builtin"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPlatformMessageReceivedHookSendsOutputs(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformMessageReceived, Name: "test.received.output", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Outputs = append(event.Outputs, delivery.Text("received output"))
		return event, nil
	})}); err != nil {
		t.Fatalf("Register received hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "received output") || !strings.Contains(got, "final") {
		t.Fatalf("platform output = %q", got)
	}
}

func TestUnwokenGroupMessageSkipsLLMButAllowsPassiveHook(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformMessageReceived, Name: "test.passive", Match: hook.Always(), Wakeup: hook.WakeupAny, Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Outputs = append(event.Outputs, delivery.Text("passive output"))
		return event, nil
	})}); err != nil {
		t.Fatalf("Register passive hook: %v", err)
	}
	a.SetHookManager(manager)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qqonebot",
		ScopeID:          "group:9",
		ConversationKind: platform.ConversationGroup,
		Sender:           p,
		RawText:          "hello",
		Segments:         []platform.MessageSegment{{Type: platform.SegmentText, Text: "hello"}},
	})

	if err := a.HandleMessage(ctx, "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := p.out.String(); !strings.Contains(got, "passive output") || strings.Contains(got, "final") {
		t.Fatalf("platform output = %q", got)
	}
	if got := len(f.chatRequests()); got != 0 {
		t.Fatalf("chat requests = %d, want 0", got)
	}
}

func TestUnwokenGroupMessageSkipsDefaultHook(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformMessageReceived, Name: "test.default", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Outputs = append(event.Outputs, delivery.Text("default output"))
		return event, nil
	})}); err != nil {
		t.Fatalf("Register default hook: %v", err)
	}
	a.SetHookManager(manager)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qqonebot",
		ScopeID:          "group:9",
		ConversationKind: platform.ConversationGroup,
		Sender:           p,
		RawText:          "hello",
		Segments:         []platform.MessageSegment{{Type: platform.SegmentText, Text: "hello"}},
	})

	if err := a.HandleMessage(ctx, "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := p.out.String(); strings.Contains(got, "default output") || strings.Contains(got, "final") {
		t.Fatalf("platform output = %q", got)
	}
	if got := len(f.chatRequests()); got != 0 {
		t.Fatalf("chat requests = %d, want 0", got)
	}
}

func TestWokenGroupMessageSkipsForbiddenHookAndRunsLLM(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformMessageReceived, Name: "test.passive-only", Match: hook.Always(), Wakeup: hook.WakeupForbidden, Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Outputs = append(event.Outputs, delivery.Text("plugin output"))
		event.Control.Consume = true
		return event, nil
	})}); err != nil {
		t.Fatalf("Register passive-only hook: %v", err)
	}
	a.SetHookManager(manager)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qqonebot",
		ScopeID:          "group:9",
		ConversationKind: platform.ConversationGroup,
		Sender:           p,
		RawText:          "芙莉丝 hello",
		Segments:         []platform.MessageSegment{{Type: platform.SegmentText, Text: "芙莉丝 hello"}},
		TriggerKeywords:  []string{"芙莉丝"},
	})

	if err := a.HandleMessage(ctx, "芙莉丝 hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := p.out.String(); strings.Contains(got, "plugin output") || !strings.Contains(got, "final") {
		t.Fatalf("platform output = %q", got)
	}
	if got := len(f.chatRequests()); got != 1 {
		t.Fatalf("chat requests = %d, want 1", got)
	}
}

func TestPassiveHookCannotWakeLLMByEditingMessage(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointPlatformMessageReceived, Name: "test.edit", Match: hook.Always(), Wakeup: hook.WakeupAny, Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Message.Segments = llm.TextSegments("芙莉丝 hello")
		return event, nil
	})}); err != nil {
		t.Fatalf("Register passive hook: %v", err)
	}
	a.SetHookManager(manager)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qqonebot",
		ScopeID:          "group:9",
		ConversationKind: platform.ConversationGroup,
		Sender:           p,
		RawText:          "hello",
		Segments:         []platform.MessageSegment{{Type: platform.SegmentText, Text: "hello"}},
		TriggerKeywords:  []string{"芙莉丝"},
	})

	if err := a.HandleMessage(ctx, "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := len(f.chatRequests()); got != 0 {
		t.Fatalf("chat requests = %d, want 0", got)
	}
}

func TestWokenGroupMessageStripsTriggerKeywordBeforeLLM(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qqonebot",
		ScopeID:          "group:9",
		ConversationKind: platform.ConversationGroup,
		Sender:           p,
		RawText:          "芙莉丝 hello",
		Segments:         []platform.MessageSegment{{Type: platform.SegmentText, Text: "芙莉丝 hello"}},
		TriggerKeywords:  []string{"芙莉丝"},
	})

	if err := a.HandleMessage(ctx, "芙莉丝 hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d, want 1", len(requests))
	}
	if got := llm.LatestUserSegmentTextOnly(requests[0].Messages); got != "hello" {
		t.Fatalf("latest user text = %q", got)
	}
}

func TestPlatformMessageReceivedHookMatchesCurrentTextWithReplyContext(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{
		Point:  hook.PointPlatformMessageReceived,
		Name:   "test.recall.reply",
		Wakeup: hook.WakeupAny,
		Match: hook.Match{Conditions: []hook.Condition{
			{Field: "message.text", Op: hook.MatchFull, Value: "撤回"},
			{Field: "message.reply.message_id", Op: hook.MatchExists},
		}},
		Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
			if event.Message.PlatformText != "撤回" {
				t.Fatalf("platform text = %q, want current text", event.Message.PlatformText)
			}
			if event.Message.Reply == nil || event.Message.Reply.Text != "通知内容" {
				t.Fatalf("reply = %#v, want structured reply", event.Message.Reply)
			}
			event.Outputs = append(event.Outputs, delivery.Text("recalled"))
			event.Control.Consume = true
			return event, nil
		}),
	}); err != nil {
		t.Fatalf("Register recall hook: %v", err)
	}
	a.SetHookManager(manager)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{
		Platform:         "qqonebot",
		ScopeID:          "group:9",
		ConversationKind: platform.ConversationGroup,
		Sender:           p,
		RawText:          "撤回",
		Segments:         []platform.MessageSegment{{Type: platform.SegmentText, Text: "撤回"}},
		ContextText:      "[引用：通知]：通知内容\n\n撤回",
		ContextSegments:  []platform.MessageSegment{{Type: platform.SegmentText, Text: "[引用：通知]：通知内容\n\n撤回"}},
		Reply:            platform.ReplyContext{MessageID: "notice-1", SenderID: "bot", Text: "通知内容", Segments: []platform.MessageSegment{{Type: platform.SegmentText, Text: "通知内容"}}},
	})

	if err := a.HandleMessage(ctx, "撤回"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := p.out.String(); got != "recalled" {
		t.Fatalf("platform output = %q, want recalled", got)
	}
	if got := len(f.chatRequests()); got != 0 {
		t.Fatalf("chat requests = %d, want 0", got)
	}
}

func TestWaitingContinuationPassThroughReachesLaterHooksAndLLM(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetHookRuntime(&fakeHookRouter{routed: true})
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{
		Point: hook.PointPlatformMessageReceived,
		Name:  "test.after.waiting",
		Match: hook.Always(),
		Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
			event.Outputs = append(event.Outputs, delivery.Text("later hook"))
			return event, nil
		}),
	}); err != nil {
		t.Fatalf("Register hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := p.out.String(); !strings.Contains(got, "later hook") || !strings.Contains(got, "final") {
		t.Fatalf("platform output = %q, want later hook and final", got)
	}
	if got := len(f.chatRequests()); got != 1 {
		t.Fatalf("chat requests = %d, want 1", got)
	}
}

func TestStreamingOutputPreparedHookReplacesFinalMessage(t *testing.T) {
	p := &fakeStreamingPlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: "猫"}}}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointAgentOutputPrepared, Name: "test.output", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Message.Segments = llm.ReplaceSegmentText(event.Message.Segments, regexp.MustCompile("猫"), "狗", true)
		return event, nil
	})}); err != nil {
		t.Fatalf("Register output hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if got := strings.Join(p.stream.appends, ""); got != "猫" {
		t.Fatalf("stream appends = %q", got)
	}
	if len(p.stream.replaces) != 1 || p.stream.replaces[0] != "狗" {
		t.Fatalf("stream replaces = %#v", p.stream.replaces)
	}
}

func TestNonStreamingPlatformSendsOnlyHookText(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{{
		{DeltaContent: "hello "},
		{DeltaContent: "[[wave]]"},
	}}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointLLMResponseReceived, Name: "test.replace", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.LLM.Text = strings.ReplaceAll(event.LLM.Text, "[[wave]]", "world")
		return event, nil
	})}); err != nil {
		t.Fatalf("Register response hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	got := p.out.String()
	if !strings.Contains(got, "hello world") || strings.Contains(got, "[[wave]]") {
		t.Fatalf("non-streaming output = %q", got)
	}
}

func TestAfterAssistantOutputsAreSentAfterFinalText(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"final text"}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointLLMResponseReceived, Name: "test.outputs", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.Outputs = append(event.Outputs,
			delivery.Text("immediate"),
			delivery.WithDeliveryTiming(delivery.Text("after"), delivery.DeliveryAfterAssistant),
		)
		return event, nil
	})}); err != nil {
		t.Fatalf("Register response hook: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	got := p.out.String()
	immediateIdx := strings.Index(got, "immediate")
	finalIdx := strings.Index(got, "final text")
	afterIdx := strings.Index(got, "after")
	if immediateIdx < 0 || finalIdx < 0 || afterIdx < 0 || !(immediateIdx < finalIdx && finalIdx < afterIdx) {
		t.Fatalf("output = %q, want immediate before final text before after", got)
	}
}

func TestLLMResponseHookRewritesOutputButPersistsRawAssistantContent(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"raw response"}}
	store := newTestStore(t)
	a := New(p, f, "m", config.ProviderConfig{}, store)
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointLLMResponseReceived, Priority: 0, Name: "test.clean-response", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		event.LLM.Text = "cleaned response"
		return event, nil
	})}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	session, err := a.sessions.Current(context.Background(), a.scope(context.Background()))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	messages, err := store.Messages().ListBySession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	got := messages[len(messages)-1].Content
	if got != "raw response" {
		t.Fatalf("assistant content = %q, want raw response", got)
	}
	if !strings.Contains(p.out.String(), "cleaned response") {
		t.Fatalf("platform output = %q, want cleaned response", p.out.String())
	}
}

func TestAgentOutputHookCanRewritePlatformOutput(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"raw"}}
	a := New(p, f, "m", config.ProviderConfig{}, newTestStore(t))
	manager := hook.NewManager()
	if err := manager.Register(hook.Registration{Point: hook.PointAgentOutputPrepared, Priority: 0, Name: "test.output", Match: hook.Always(), Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
		if llm.SegmentsTextOnly(event.Message.Segments) == "raw" {
			event.Message.Segments = llm.ReplaceSegmentText(event.Message.Segments, regexp.MustCompile(regexp.QuoteMeta("raw")), "visible", false)
		}
		return event, nil
	})}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !strings.Contains(p.out.String(), "visible") {
		t.Fatalf("platform output = %q, want rewritten output", p.out.String())
	}
}

func TestEmoticonHookSendsSeparateOutputAndCleansPersistedContent(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{replies: []string{"[[微笑]] 像这样~"}}
	store := newTestStore(t)
	a := New(p, f, "m", config.ProviderConfig{}, store)
	manager := hook.NewManager()
	configDir := t.TempDir()
	emoticonDir := filepath.Join(configDir, "emoticons", "微笑")
	if err := os.MkdirAll(emoticonDir, 0o755); err != nil {
		t.Fatalf("mkdir emoticon dir: %v", err)
	}
	imgPath := filepath.Join(emoticonDir, "01.png")
	if err := os.WriteFile(imgPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write emoticon image: %v", err)
	}
	scriptPath := filepath.Join(configDir, "emoticon_extract.sh")
	script := `#!/bin/sh
read init
printf '{"type":"response","id":"host:init","ok":true,"result":{}}\n'
read event
img=$(ls emoticons/微笑/*.png 2>/dev/null | head -1)
printf '{"type":"response","id":"host:event","ok":true,"result":{"status":"completed","outputs":[{"kind":"image","name":"微笑","path":"%s"}],"message":{"text":"像这样~"}}}\n' "$img"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	hooksTOML := `
[[rules]]
name = "emoticon_extract"
on = "llm.response.received"
priority = 1000
if = "llm.text"
op = "regex"
value = "\\[\\[[^\\[\\]]+\\]\\]"
actions = [
  { action_name = "extract", type = "exec", command = ["sh", "./emoticon_extract.sh"], field = "llm.text" },
]
`
	if err := os.WriteFile(filepath.Join(configDir, "hooks.toml"), []byte(hooksTOML), 0o644); err != nil {
		t.Fatalf("write hooks.toml: %v", err)
	}
	if _, err := hookbuiltin.RegisterAll(manager, hookbuiltin.Options{ConfigDir: configDir}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	a.SetHookManager(manager)

	if err := a.HandleMessage(context.Background(), "说个微笑"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	out := p.out.String()
	emoticonIdx := strings.Index(out, "[图片: 微笑]")
	textIdx := strings.Index(out, "像这样~")
	if emoticonIdx < 0 || textIdx < 0 || emoticonIdx > textIdx {
		t.Fatalf("platform output = %q, want separate emoticon fallback and cleaned text", out)
	}
	if strings.Contains(out, "[[微笑]]") {
		t.Fatalf("platform output still contains raw token: %q", out)
	}
	session, err := a.sessions.Current(context.Background(), a.scope(context.Background()))
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	messages, err := store.Messages().ListBySession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	got := messages[len(messages)-1].Content
	if got != "[[微笑]] 像这样~" {
		t.Fatalf("assistant content = %q, want raw text", got)
	}
}
