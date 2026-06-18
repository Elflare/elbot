package hook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/llm"
)

// Point names a stable boundary where hooks may inspect or update an event.
type Point string

const (
	PointPlatformConnected       Point = "platform.connected"
	PointPlatformMessageReceived Point = "platform.message.received"
	PointAgentInputPrepared      Point = "agent.input.prepared"
	PointLLMTurnPrepared         Point = "llm.turn.prepared"
	PointLLMRequestPrepared      Point = "llm.request.prepared"
	PointLLMResponseReceived     Point = "llm.response.received"
	PointToolCallPrepared        Point = "tool.call.prepared"
	PointToolCallCompleted       Point = "tool.call.completed"
	PointAgentOutputPrepared     Point = "agent.output.prepared"
	PointAgentTurnOutputPrepared Point = "agent.turn.output.prepared"
	PointPlatformMessageSent     Point = "platform.message.sent"
	PointErrorOccurred           Point = "error.occurred"
)

func KnownPoint(point Point) bool {
	switch point {
	case PointPlatformConnected,
		PointPlatformMessageReceived,
		PointAgentInputPrepared,
		PointLLMTurnPrepared,
		PointLLMRequestPrepared,
		PointLLMResponseReceived,
		PointToolCallPrepared,
		PointToolCallCompleted,
		PointAgentOutputPrepared,
		PointAgentTurnOutputPrepared,
		PointPlatformMessageSent,
		PointErrorOccurred:
		return true
	default:
		return false
	}
}

// Event carries the context available at a hook point. Fields are populated
// according to Point; hook handlers should only rely on fields relevant there.
type Event struct {
	ID       string
	Point    Point
	Time     time.Time
	Metadata map[string]any

	Platform PlatformContext
	Actor    ActorContext
	Session  SessionContext
	Request  RequestContext
	Message  MessagePayload
	LLM      LLMPayload
	Tool     ToolPayload
	Outputs  []delivery.Output
	Error    error
}

type PlatformContext struct {
	Name              string
	ScopeID           string
	UserID            string
	ConversationID    string
	PlatformMessageID string
	ReplyToMessageID  string
}

type ActorContext struct {
	ID          string
	Role        string
	UserID      string
	DisplayName string
}

type SessionContext struct {
	ID     string
	Mode   string
	Title  string
	Status string
}

type RequestContext struct {
	ID        string
	Kind      string
	SessionID string
	Phase     string
}

type MessagePayload struct {
	ID       string
	Role     string
	Segments []llm.MessageSegment
	Messages []llm.LLMMessage
}

type LLMPayload struct {
	Provider  string
	Model     string
	Messages  []llm.LLMMessage
	Tools     []llm.ToolSchema
	Usage     *llm.Usage
	RawText   string
	Text      string
	ToolCalls []llm.ToolCallRequest
	ElapsedMS int64
}

type ToolPayload struct {
	ID        string
	Name      string
	Arguments string
	Risk      string
	Result    string
	Error     error
}

// Handler processes one hook event and may return an updated event.
type Handler interface {
	HandleHook(ctx context.Context, event Event) (Event, error)
}

type HandlerFunc func(ctx context.Context, event Event) (Event, error)

func (fn HandlerFunc) HandleHook(ctx context.Context, event Event) (Event, error) {
	return fn(ctx, event)
}

const (
	MatchAlways   = "always"
	MatchExists   = "exists"
	MatchContains = "contains"
	MatchFull     = "fullmatch"
	MatchPrefix   = "startswith"
	MatchSuffix   = "endswith"
	MatchRegex    = "regex"
)

// Match is an explicit AND-list of conditions required before a hook runs.
type Match struct {
	Conditions []Condition
}

type Condition struct {
	Field string `toml:"field"`
	Op    string `toml:"op"`
	Value string `toml:"value"`
}

func Always() Match {
	return Match{Conditions: []Condition{{Op: MatchAlways}}}
}

func Contains(field, value string) Match {
	return Match{Conditions: []Condition{{Field: field, Op: MatchContains, Value: value}}}
}

func FullMatch(field, value string) Match {
	return Match{Conditions: []Condition{{Field: field, Op: MatchFull, Value: value}}}
}

func StartsWith(field, value string) Match {
	return Match{Conditions: []Condition{{Field: field, Op: MatchPrefix, Value: value}}}
}

func EndsWith(field, value string) Match {
	return Match{Conditions: []Condition{{Field: field, Op: MatchSuffix, Value: value}}}
}

func Regex(field, value string) Match {
	return Match{Conditions: []Condition{{Field: field, Op: MatchRegex, Value: value}}}
}

func Exists(field string) Match {
	return Match{Conditions: []Condition{{Field: field, Op: MatchExists}}}
}

func (m Match) Validate() error {
	if len(m.Conditions) == 0 {
		return fmt.Errorf("hook match requires at least one condition")
	}
	for i, cond := range m.Conditions {
		if err := cond.validate(); err != nil {
			return fmt.Errorf("condition %d: %w", i+1, err)
		}
	}
	return nil
}

func (m Match) Matches(event Event) bool {
	for _, cond := range m.Conditions {
		if !cond.matches(event) {
			return false
		}
	}
	return true
}

func (c Condition) validate() error {
	op := strings.TrimSpace(c.Op)
	if op == "" {
		return fmt.Errorf("op is required")
	}
	if !knownMatchOp(op) {
		return fmt.Errorf("unsupported op %q", op)
	}
	if op == MatchAlways {
		if strings.TrimSpace(c.Field) != "" || strings.TrimSpace(c.Value) != "" {
			return fmt.Errorf("always cannot set field or value")
		}
		return nil
	}
	if !knownMatchField(c.Field) {
		return fmt.Errorf("unsupported field %q", c.Field)
	}
	if needsMatchValue(op) && c.Value == "" {
		return fmt.Errorf("value is required for %s", op)
	}
	if op == MatchRegex {
		if _, err := regexp.Compile(c.Value); err != nil {
			return fmt.Errorf("invalid regex: %w", err)
		}
	}
	return nil
}

func (c Condition) matches(event Event) bool {
	switch strings.TrimSpace(c.Op) {
	case MatchAlways:
		return true
	case MatchExists:
		return matchField(event, c.Field) != ""
	case MatchContains:
		return strings.Contains(matchField(event, c.Field), c.Value)
	case MatchFull:
		return matchField(event, c.Field) == c.Value
	case MatchPrefix:
		return strings.HasPrefix(matchField(event, c.Field), c.Value)
	case MatchSuffix:
		return strings.HasSuffix(matchField(event, c.Field), c.Value)
	case MatchRegex:
		ok, _ := regexp.MatchString(c.Value, matchField(event, c.Field))
		return ok
	default:
		return false
	}
}

func knownMatchOp(op string) bool {
	switch op {
	case MatchAlways, MatchExists, MatchContains, MatchFull, MatchPrefix, MatchSuffix, MatchRegex:
		return true
	default:
		return false
	}
}

func needsMatchValue(op string) bool {
	switch op {
	case MatchContains, MatchFull, MatchPrefix, MatchSuffix, MatchRegex:
		return true
	default:
		return false
	}
}

func knownMatchField(field string) bool {
	switch strings.TrimSpace(field) {
	case "platform.name", "platform.scope_id", "platform.user_id", "platform.conversation_id", "platform.message_id", "platform.reply_to_message_id",
		"message.text", "message.content_text", "message.role",
		"llm.text", "llm.raw_text", "llm.latest_user_text", "llm.latest_user_content_text", "llm.provider", "llm.model",
		"tool.name", "tool.arguments", "tool.result", "tool.risk",
		"actor.id", "actor.user_id", "actor.role", "actor.display_name",
		"session.id", "session.mode", "session.status",
		"request.id", "request.kind", "request.phase":
		return true
	default:
		return false
	}
}

func matchField(event Event, field string) string {
	switch strings.TrimSpace(field) {
	case "platform.name":
		return event.Platform.Name
	case "platform.scope_id":
		return event.Platform.ScopeID
	case "platform.user_id":
		return event.Platform.UserID
	case "platform.conversation_id":
		return event.Platform.ConversationID
	case "platform.message_id":
		return event.Platform.PlatformMessageID
	case "platform.reply_to_message_id":
		return event.Platform.ReplyToMessageID
	case "message.text":
		return llm.SegmentsTextOnly(event.Message.Segments)
	case "message.content_text":
		return llm.SegmentsContentText(event.Message.Segments)
	case "message.role":
		return event.Message.Role
	case "llm.text":
		return event.LLM.Text
	case "llm.raw_text":
		return event.LLM.RawText
	case "llm.latest_user_text":
		return llm.LatestUserSegmentTextOnly(event.LLM.Messages)
	case "llm.latest_user_content_text":
		return llm.LatestUserSegmentContentText(event.LLM.Messages)
	case "llm.provider":
		return event.LLM.Provider
	case "llm.model":
		return event.LLM.Model
	case "tool.name":
		return event.Tool.Name
	case "tool.arguments":
		return event.Tool.Arguments
	case "tool.result":
		return event.Tool.Result
	case "tool.risk":
		return event.Tool.Risk
	case "actor.id":
		return event.Actor.ID
	case "actor.user_id":
		return event.Actor.UserID
	case "actor.role":
		return event.Actor.Role
	case "actor.display_name":
		return event.Actor.DisplayName
	case "session.id":
		return event.Session.ID
	case "session.mode":
		return event.Session.Mode
	case "session.status":
		return event.Session.Status
	case "request.id":
		return event.Request.ID
	case "request.kind":
		return event.Request.Kind
	case "request.phase":
		return event.Request.Phase
	default:
		return ""
	}
}

// Registration describes one explicitly matched hook handler.
type Registration struct {
	Point    Point
	Priority int
	Name     string
	Match    Match
	Handler  Handler
}

// Registrar registers hook handlers for modules.
type Registrar interface {
	Register(reg Registration) error
}

// Module contributes one or more handlers to a hook registrar.
type Module interface {
	RegisterHooks(registrar Registrar) error
}

// Manager runs handlers for hook points in priority order.
type Manager interface {
	Registrar
	Run(ctx context.Context, event Event) (Event, error)
	Notify(ctx context.Context, event Event) error
}

// NoopManager is used when no hooks are configured.
type NoopManager struct{}

func (NoopManager) Register(Registration) error { return nil }

func (NoopManager) Run(_ context.Context, event Event) (Event, error) {
	return prepareEvent(event), nil
}

func (NoopManager) Notify(context.Context, Event) error { return nil }

type DefaultManager struct {
	mu       sync.RWMutex
	next     int
	handlers map[Point][]registration
	logger   *slog.Logger
}

type registration struct {
	priority int
	order    int
	name     string
	match    Match
	handler  Handler
}

func NewManager() *DefaultManager {
	return &DefaultManager{handlers: map[Point][]registration{}}
}

func (m *DefaultManager) SetLogger(logger *slog.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logger = logger
}

func (m *DefaultManager) Register(reg Registration) error {
	if err := validateRegistration(reg); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	m.handlers[reg.Point] = append(m.handlers[reg.Point], registration{priority: reg.Priority, order: m.next, name: reg.Name, match: reg.Match, handler: reg.Handler})
	sort.SliceStable(m.handlers[reg.Point], func(i, j int) bool {
		left := m.handlers[reg.Point][i]
		right := m.handlers[reg.Point][j]
		if left.priority == right.priority {
			return left.order < right.order
		}
		return left.priority < right.priority
	})
	return nil
}

func validateRegistration(reg Registration) error {
	if reg.Point == "" {
		return fmt.Errorf("hook point is required")
	}
	if strings.TrimSpace(reg.Name) == "" {
		return fmt.Errorf("hook name is required")
	}
	if reg.Handler == nil {
		return fmt.Errorf("hook handler is required")
	}
	if err := reg.Match.Validate(); err != nil {
		return fmt.Errorf("hook %q match: %w", reg.Name, err)
	}
	return nil
}

func (m *DefaultManager) Run(ctx context.Context, event Event) (Event, error) {
	event = prepareEvent(event)
	for _, reg := range m.handlersFor(event.Point) {
		if !reg.match.Matches(event) {
			continue
		}
		before := event
		updated, err := reg.handler.HandleHook(ctx, event)
		if err != nil {
			m.logHook(ctx, "run", reg, before, before, err, true)
			return event, wrapHookError(reg, err)
		}
		updated = markHookOutputs(updated, len(before.Outputs), reg, "run")
		event = prepareEvent(updated)
		m.logHook(ctx, "run", reg, before, event, nil, true)
	}
	return event, nil
}

func (m *DefaultManager) Notify(ctx context.Context, event Event) error {
	event = prepareEvent(event)
	var joined error
	for _, reg := range m.handlersFor(event.Point) {
		if !reg.match.Matches(event) {
			continue
		}
		before := event
		updated, err := reg.handler.HandleHook(ctx, event)
		if err != nil {
			m.logHook(ctx, "notify", reg, before, before, err, true)
			joined = errors.Join(joined, wrapHookError(reg, err))
			continue
		}
		_ = markHookOutputs(updated, len(before.Outputs), reg, "notify")
		m.logHook(ctx, "notify", reg, before, before, nil, true)
	}
	return joined
}

func (m *DefaultManager) handlersFor(point Point) []registration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := m.handlers[point]
	out := make([]registration, len(items))
	copy(out, items)
	return out
}

func wrapHookError(reg registration, err error) error {
	if err == nil {
		return nil
	}
	if strings.TrimSpace(reg.name) == "" {
		return err
	}
	return fmt.Errorf("hook %s: %w", reg.name, err)
}

func markHookOutputs(event Event, start int, reg registration, mode string) Event {
	if start < 0 {
		start = 0
	}
	if start > len(event.Outputs) {
		start = len(event.Outputs)
	}
	for i := start; i < len(event.Outputs); i++ {
		meta := event.Outputs[i].Meta
		if meta == nil {
			meta = map[string]any{}
		}
		setOutputMeta(meta, delivery.MetaHookPoint, string(event.Point))
		setOutputMeta(meta, delivery.MetaHookName, reg.name)
		setOutputMeta(meta, delivery.MetaHookMode, mode)
		event.Outputs[i].Meta = meta
	}
	return event
}

func setOutputMeta(meta map[string]any, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	if _, exists := meta[key]; exists {
		return
	}
	meta[key] = value
}

func (m *DefaultManager) logHook(ctx context.Context, mode string, reg registration, before, after Event, err error, completed bool) {
	logger := m.loggerForLog()
	if logger == nil || !completed {
		return
	}
	attrs := []any{
		"point", string(before.Point),
		"hook", reg.name,
		"priority", reg.priority,
		"order", reg.order,
		"mode", mode,
	}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}
	logger.InfoContext(ctx, "hook triggered", attrs...)
	debugAttrs := append([]any{}, attrs...)
	debugAttrs = append(debugAttrs,
		"session_id", after.Session.ID,
		"provider", after.LLM.Provider,
		"model", after.LLM.Model,
		"tool", after.Tool.Name,
		"before_text", trimLogText(eventText(before)),
		"after_text", trimLogText(eventText(after)),
		"raw_text", trimLogText(after.LLM.RawText),
	)
	logger.DebugContext(ctx, "hook event", debugAttrs...)
}

func (m *DefaultManager) loggerForLog() *slog.Logger {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.logger
}

func eventText(event Event) string {
	switch {
	case event.LLM.Text != "":
		return event.LLM.Text
	case llm.SegmentsContentText(event.Message.Segments) != "":
		return llm.SegmentsContentText(event.Message.Segments)
	case event.Tool.Result != "":
		return event.Tool.Result
	case event.Tool.Arguments != "":
		return event.Tool.Arguments
	default:
		return ""
	}
}

func trimLogText(text string) string {
	const max = 300
	if len([]rune(text)) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + "..."
}

func prepareEvent(event Event) Event {
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	if event.Metadata == nil {
		event.Metadata = map[string]any{}
	}
	return event
}
