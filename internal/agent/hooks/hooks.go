package hooks

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"fmt"
	"sync"
)

type HookIdentity interface {
	SourceActor(context.Context) contextinfo.Actor
}
type HookBridgeDeps struct {
	Identity HookIdentity // Source facts supplied by Agent; optional for source-free use.
}

// HookBridge owns callback admission, not the provider's workers or rules.
type HookBridge struct {
	mu       sync.Mutex
	points   *Points // Current registration; detached callbacks retain their snapshot.
	identity HookIdentity
	active   int           // Admitted invocations, including callbacks from detached registrations.
	closed   bool          // Final dispatch shutdown, after Agent producers have drained.
	done     chan struct{} // Closed only after admission stops and active reaches zero.
}

func NewHookBridge(deps HookBridgeDeps) *HookBridge {
	return &HookBridge{identity: deps.Identity, done: make(chan struct{})}
}

func (h *HookBridge) Register(points Points) (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, context.Canceled
	}
	if h.points != nil {
		return nil, fmt.Errorf("agent hooks: provider already registered")
	}
	h.points = &points
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.points == &points {
			h.points = nil
		}
	}, nil
}

func (h *HookBridge) acquire() (*Points, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, nil, context.Canceled
	}
	if h.points == nil {
		return nil, nil, nil
	}
	points := h.points
	h.active++
	return points, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.active--
		if h.closed && h.active == 0 {
			close(h.done)
		}
	}, nil
}

func (h *HookBridge) facts(ctx context.Context, facts Facts) Facts {
	if h.identity != nil {
		facts.Actor = h.identity.SourceActor(ctx)
	}
	facts.Woken = agentshare.WakeupFromContext(ctx)
	facts.ReadOnlyCalls = agentshare.ReadOnlyCallsFromContext(ctx)
	return facts
}

// prepareMessage supplies Agent input facts; conversion to a provider protocol
// (including original platform payload and reply data) belongs to the provider.
func prepareMessage(ctx context.Context, message agentshare.Message) agentshare.Message {
	if message.PlatformText == "" {
		if incoming, ok := agentshare.MessageContextFrom(ctx); ok {
			message.PlatformText = incoming.RawText
		}
	}
	if message.IntentText == "" && message.Role == string(llm.RoleUser) {
		message.IntentText = agentshare.StripWakeupPrefix(ctx, llm.SegmentsTextOnly(message.Segments))
	}
	return message
}

// BeginClose is called only after Agent's producers finish, not at the start of
// application shutdown: those producers may still invoke callbacks while draining.
func (h *HookBridge) BeginClose() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed, h.points = true, nil
	if h.active == 0 {
		close(h.done)
	}
}
func (h *HookBridge) Done() <-chan struct{} { return h.done }
func (h *HookBridge) Close(ctx context.Context) error {
	h.BeginClose()
	select {
	case <-h.done:
		return nil
	default:
	}
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func invoke[T, R any](h *HookBridge, ctx context.Context, facts Facts, value T, fallback R, selectPoint func(*Points) func(context.Context, Facts, T) (R, events.LogResult, error)) (R, events.LogResult, error) {
	points, done, err := h.acquire()
	if err != nil {
		return fallback, events.LogResult{ResultStatus: events.ResultCanceled}, err
	}
	if points != nil {
		defer done()
		if fn := selectPoint(points); fn != nil {
			return fn(ctx, h.facts(ctx, facts), value)
		}
	}
	return fallback, events.LogResult{ResultStatus: events.ResultSkipped}, nil
}

func (h *HookBridge) HasRoute(ctx context.Context, message agentshare.Message) bool {
	points, done, _ := h.acquire()
	if points == nil {
		return false
	}
	defer done()
	return points.HasRoute != nil && points.HasRoute(ctx, h.facts(ctx, Facts{}), prepareMessage(ctx, message))
}

func (h *HookBridge) CancelRoute(ctx context.Context, message agentshare.Message) bool {
	points, done, _ := h.acquire()
	if points == nil {
		return false
	}
	defer done()
	return points.CancelRoute != nil && points.CancelRoute(ctx, h.facts(ctx, Facts{}), prepareMessage(ctx, message))
}

func (h *HookBridge) PlatformMessageReceived(ctx context.Context, value agentshare.Message) (agentshare.PlatformMessageReceivedResult, events.LogResult, error) {
	value = prepareMessage(ctx, value)
	return invoke(h, ctx, Facts{}, value, agentshare.PlatformMessageReceivedResult{Segments: value.Segments}, func(p *Points) func(context.Context, Facts, agentshare.Message) (agentshare.PlatformMessageReceivedResult, events.LogResult, error) {
		return p.PlatformMessageReceived
	})
}

func (h *HookBridge) AgentInputPrepared(ctx context.Context, session agentshare.Session, value agentshare.Message) ([]llm.MessageSegment, events.LogResult, error) {
	value = prepareMessage(ctx, value)
	return invoke(h, ctx, Facts{Session: session}, value, value.Segments, func(p *Points) func(context.Context, Facts, agentshare.Message) ([]llm.MessageSegment, events.LogResult, error) {
		return p.AgentInputPrepared
	})
}

func (h *HookBridge) LLMTurnPrepared(ctx context.Context, session agentshare.Session, value agentshare.LLMInput) (agentshare.LLMInputResult, events.LogResult, error) {
	value.Message = prepareMessage(ctx, value.Message)
	return invoke(h, ctx, Facts{Session: session}, value, agentshare.LLMInputResult{Segments: value.Message.Segments, Tools: value.Tools}, func(p *Points) func(context.Context, Facts, agentshare.LLMInput) (agentshare.LLMInputResult, events.LogResult, error) {
		return p.LLMTurnPrepared
	})
}

func (h *HookBridge) LLMRequestPrepared(ctx context.Context, session agentshare.Session, value agentshare.LLMInput) (agentshare.LLMInputResult, events.LogResult, error) {
	value.Message = prepareMessage(ctx, value.Message)
	return invoke(h, ctx, Facts{Session: session}, value, agentshare.LLMInputResult{Segments: value.Message.Segments, Tools: value.Tools}, func(p *Points) func(context.Context, Facts, agentshare.LLMInput) (agentshare.LLMInputResult, events.LogResult, error) {
		return p.LLMRequestPrepared
	})
}

func (h *HookBridge) LLMResponseReceived(ctx context.Context, session agentshare.Session, value agentshare.LLMResponse) (agentshare.LLMResponseResult, events.LogResult, error) {
	return invoke(h, ctx, Facts{Session: session}, value, agentshare.LLMResponseResult{Text: value.Text, SourceText: value.SourceText, Usage: value.Usage, ToolCalls: value.ToolCalls}, func(p *Points) func(context.Context, Facts, agentshare.LLMResponse) (agentshare.LLMResponseResult, events.LogResult, error) {
		return p.LLMResponseReceived
	})
}

func (h *HookBridge) ToolCallPrepared(ctx context.Context, session agentshare.Session, value llm.ToolCallRequest) (string, events.LogResult, error) {
	return invoke(h, ctx, Facts{Session: session}, value, value.Arguments, func(p *Points) func(context.Context, Facts, llm.ToolCallRequest) (string, events.LogResult, error) {
		return p.ToolCallPrepared
	})
}

func (h *HookBridge) ToolCallCompleted(ctx context.Context, session agentshare.Session, value agentshare.ToolResult) ([]llm.MessageSegment, events.LogResult, error) {
	return invoke(h, ctx, Facts{Session: session}, value, value.Segments, func(p *Points) func(context.Context, Facts, agentshare.ToolResult) ([]llm.MessageSegment, events.LogResult, error) {
		return p.ToolCallCompleted
	})
}

func (h *HookBridge) AgentOutputPrepared(ctx context.Context, value string) (string, events.LogResult, error) {
	return invoke(h, ctx, Facts{}, value, value, func(p *Points) func(context.Context, Facts, string) (string, events.LogResult, error) {
		return p.AgentOutputPrepared
	})
}

func (h *HookBridge) AgentTurnOutputPrepared(ctx context.Context, value string) (string, events.LogResult, error) {
	return invoke(h, ctx, Facts{}, value, value, func(p *Points) func(context.Context, Facts, string) (string, events.LogResult, error) {
		return p.AgentTurnOutputPrepared
	})
}

func (h *HookBridge) PlatformMessageSent(ctx context.Context, text string) {
	points, done, _ := h.acquire()
	if points == nil {
		return
	}
	defer done()
	if points.PlatformMessageSent != nil {
		points.PlatformMessageSent(ctx, h.facts(ctx, Facts{}), text)
	}
}
func (h *HookBridge) ErrorOccurred(ctx context.Context, session agentshare.Session, response agentshare.LLMResponse, err error) {
	points, done, _ := h.acquire()
	if points == nil {
		return
	}
	defer done()
	if points.ErrorOccurred != nil {
		points.ErrorOccurred(ctx, h.facts(ctx, Facts{Session: session}), response, err)
	}
}
