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
	ID       string         `json:"id"`
	Point    Point          `json:"point"`
	Time     time.Time      `json:"time"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Control  Control        `json:"control"`

	Platform  PlatformContext   `json:"platform"`
	Actor     ActorContext      `json:"actor"`
	Session   SessionContext    `json:"session"`
	Request   RequestContext    `json:"request"`
	Message   MessagePayload    `json:"message"`
	LLM       LLMPayload        `json:"llm"`
	Tool      ToolPayload       `json:"tool"`
	Outputs   []delivery.Output `json:"outputs,omitempty"`
	Error     error             `json:"-"`
	ErrorInfo *ErrorPayload     `json:"error,omitempty"`
}

type Control struct {
	Consume         bool `json:"consume"`
	StopPropagation bool `json:"stop_propagation"`
}

type ErrorPayload struct {
	Message string `json:"message"`
}

type PlatformContext struct {
	Name              string `json:"name"`
	ScopeID           string `json:"scope_id"`
	UserID            string `json:"user_id"`
	ConversationID    string `json:"conversation_id"`
	PlatformMessageID string `json:"message_id"`
	ReplyToMessageID  string `json:"reply_to_message_id"`
}

type ActorContext struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	GroupRole   string `json:"group_role"`
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

type SessionContext struct {
	ID     string `json:"id"`
	Mode   string `json:"mode"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type RequestContext struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	Phase     string `json:"phase"`
}

type MessagePayload struct {
	ID        string               `json:"id"`
	Role      string               `json:"role"`
	RawText   string               `json:"raw_text,omitempty"`
	InputText string               `json:"input_text,omitempty"`
	Reply     *MessageReplyPayload `json:"reply,omitempty"`
	Segments  []llm.MessageSegment `json:"segments,omitempty"`
	Messages  []llm.LLMMessage     `json:"messages,omitempty"`
}

type MessageReplyPayload struct {
	MessageID   string               `json:"message_id"`
	SenderID    string               `json:"sender_id,omitempty"`
	Text        string               `json:"text,omitempty"`
	ContentText string               `json:"content_text,omitempty"`
	Segments    []llm.MessageSegment `json:"segments,omitempty"`
}

type LLMPayload struct {
	Provider  string                `json:"provider"`
	Model     string                `json:"model"`
	Messages  []llm.LLMMessage      `json:"messages,omitempty"`
	Tools     []llm.ToolSchema      `json:"tools,omitempty"`
	Usage     *llm.Usage            `json:"usage,omitempty"`
	RawText   string                `json:"raw_text,omitempty"`
	Text      string                `json:"text,omitempty"`
	ToolCalls []llm.ToolCallRequest `json:"tool_calls,omitempty"`
	ElapsedMS int64                 `json:"elapsed_ms,omitempty"`
}

type ToolPayload struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
	Risk      string `json:"risk,omitempty"`
	Result    string `json:"result,omitempty"`
	Error     error  `json:"error,omitempty"`
}

type RegexMatch struct {
	Field  string            `json:"field"`
	Value  string            `json:"value"`
	Text   string            `json:"text"`
	Groups []string          `json:"groups"`
	Named  map[string]string `json:"named,omitempty"`
	Start  int               `json:"start"`
	End    int               `json:"end"`
}

type MatchContext struct {
	Regex []RegexMatch `json:"regex,omitempty"`
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
	matched := m.MatchEvent(event)
	return matched.OK
}

type MatchResult struct {
	OK      bool
	Context MatchContext
}

func (m Match) MatchEvent(event Event) MatchResult {
	var ctx MatchContext
	for _, cond := range m.Conditions {
		ok, capture := cond.match(event)
		if !ok {
			return MatchResult{}
		}
		if capture != nil {
			ctx.Regex = append(ctx.Regex, *capture)
		}
	}
	return MatchResult{OK: true, Context: ctx}
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
	ok, _ := c.match(event)
	return ok
}

func (c Condition) match(event Event) (bool, *RegexMatch) {
	fieldValue := matchField(event, c.Field)
	switch strings.TrimSpace(c.Op) {
	case MatchAlways:
		return true, nil
	case MatchExists:
		return fieldValue != "", nil
	case MatchContains:
		return strings.Contains(fieldValue, c.Value), nil
	case MatchFull:
		return fieldValue == c.Value, nil
	case MatchPrefix:
		return strings.HasPrefix(fieldValue, c.Value), nil
	case MatchSuffix:
		return strings.HasSuffix(fieldValue, c.Value), nil
	case MatchRegex:
		capture, ok := regexMatchContext(c.Field, c.Value, fieldValue)
		if !ok {
			return false, nil
		}
		return true, &capture
	default:
		return false, nil
	}
}

func regexMatchContext(field, pattern, value string) (RegexMatch, bool) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return RegexMatch{}, false
	}
	indexes := re.FindStringSubmatchIndex(value)
	if indexes == nil {
		return RegexMatch{}, false
	}
	names := re.SubexpNames()
	groups := make([]string, 0, len(indexes)/2)
	named := map[string]string{}
	for i := 0; i < len(indexes); i += 2 {
		start, end := indexes[i], indexes[i+1]
		group := ""
		if start >= 0 && end >= 0 {
			group = value[start:end]
		}
		groups = append(groups, group)
		nameIndex := i / 2
		if nameIndex < len(names) && strings.TrimSpace(names[nameIndex]) != "" {
			named[names[nameIndex]] = group
		}
	}
	if len(named) == 0 {
		named = nil
	}
	return RegexMatch{Field: field, Value: pattern, Text: groups[0], Groups: groups, Named: named, Start: indexes[0], End: indexes[1]}, true
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
		"message.text", "message.content_text", "message.raw_text", "message.input_text", "message.role",
		"message.reply.message_id", "message.reply.sender_id", "message.reply.text", "message.reply.content_text",
		"llm.text", "llm.raw_text", "llm.latest_user_text", "llm.latest_user_content_text", "llm.provider", "llm.model",
		"tool.name", "tool.arguments", "tool.result", "tool.risk",
		"actor.id", "actor.user_id", "actor.role", "actor.group_role", "actor.display_name",
		"session.id", "session.mode", "session.status",
		"request.id", "request.kind", "request.phase",
		"error.message":
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
	case "message.raw_text":
		return event.Message.RawText
	case "message.input_text":
		return MessageInputText(event)
	case "message.role":
		return event.Message.Role
	case "message.reply.message_id":
		if event.Message.Reply == nil {
			return ""
		}
		return event.Message.Reply.MessageID
	case "message.reply.sender_id":
		if event.Message.Reply == nil {
			return ""
		}
		return event.Message.Reply.SenderID
	case "message.reply.text":
		if event.Message.Reply == nil {
			return ""
		}
		return event.Message.Reply.Text
	case "message.reply.content_text":
		if event.Message.Reply == nil {
			return ""
		}
		return event.Message.Reply.ContentText
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
	case "actor.group_role":
		return event.Actor.GroupRole
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
	case "error.message":
		return EventErrorMessage(event)
	default:
		return ""
	}
}

// Registration describes one explicitly matched hook handler.
type Registration struct {
	Point         Point
	Priority      int
	Name          string
	Match         Match
	Handler       Handler
	Detail        string
	RequireWakeup *bool
}

// Info describes a registered hook for inspection commands.
type Info struct {
	Name     string
	Point    Point
	Priority int
	Detail   string
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
	List() []Info
	Reset()
}

// NoopManager is used when no hooks are configured.
type NoopManager struct{}

func (NoopManager) Register(Registration) error { return nil }

func (NoopManager) Run(_ context.Context, event Event) (Event, error) {
	return prepareEvent(event), nil
}

func (NoopManager) Notify(context.Context, Event) error { return nil }

func (NoopManager) List() []Info { return nil }

func (NoopManager) Reset() {}

type DefaultManager struct {
	mu       sync.RWMutex
	next     int
	handlers map[Point][]registration
	logger   *slog.Logger
	wakeup   WakeupFunc
}

type WakeupFunc func(context.Context, Event) bool

type registration struct {
	priority      int
	order         int
	name          string
	match         Match
	handler       Handler
	detail        string
	requireWakeup bool
}

func NewManager() *DefaultManager {
	return &DefaultManager{handlers: map[Point][]registration{}}
}

func (m *DefaultManager) SetLogger(logger *slog.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logger = logger
}

func (m *DefaultManager) SetWakeupFunc(fn WakeupFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wakeup = fn
}

func (m *DefaultManager) Register(reg Registration) error {
	if err := validateRegistration(reg); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	m.handlers[reg.Point] = append(m.handlers[reg.Point], registration{priority: reg.Priority, order: m.next, name: reg.Name, match: reg.Match, handler: reg.Handler, detail: reg.Detail, requireWakeup: registrationRequireWakeup(reg)})
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

func registrationRequireWakeup(reg Registration) bool {
	return reg.RequireWakeup == nil || *reg.RequireWakeup
}

func (m *DefaultManager) Run(ctx context.Context, event Event) (Event, error) {
	event = prepareEvent(event)
	for _, reg := range m.handlersFor(event.Point) {
		if m.skipForWakeup(ctx, event, reg) {
			continue
		}
		matchResult := reg.match.MatchEvent(event)
		if !matchResult.OK {
			continue
		}
		event.Metadata["match"] = matchResult.Context
		before := event
		updated, err := reg.handler.HandleHook(ctx, event)
		delete(event.Metadata, "match")
		if err != nil {
			m.logHook(ctx, "run", reg, before, before, err)
			return event, wrapHookError(reg, err)
		}
		updated = markHookOutputs(updated, len(before.Outputs), reg, "run")
		event = prepareEvent(updated)
		m.logHook(ctx, "run", reg, before, event, nil)
		if event.Control.StopPropagation {
			break
		}
	}
	return event, nil
}

func (m *DefaultManager) Notify(ctx context.Context, event Event) error {
	event = prepareEvent(event)
	var joined error
	for _, reg := range m.handlersFor(event.Point) {
		if m.skipForWakeup(ctx, event, reg) {
			continue
		}
		matchResult := reg.match.MatchEvent(event)
		if !matchResult.OK {
			continue
		}
		event.Metadata["match"] = matchResult.Context
		before := event
		updated, err := reg.handler.HandleHook(ctx, event)
		delete(event.Metadata, "match")
		if err != nil {
			m.logHook(ctx, "notify", reg, before, before, err)
			joined = errors.Join(joined, wrapHookError(reg, err))
			continue
		}
		_ = markHookOutputs(updated, len(before.Outputs), reg, "notify")
		m.logHook(ctx, "notify", reg, before, before, nil)
		if updated.Control.StopPropagation {
			break
		}
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

func (m *DefaultManager) skipForWakeup(ctx context.Context, event Event, reg registration) bool {
	if !reg.requireWakeup {
		return false
	}
	wakeup := m.wakeupForRun()
	if wakeup == nil {
		return false
	}
	return !wakeup(ctx, event)
}

func (m *DefaultManager) wakeupForRun() WakeupFunc {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.wakeup
}

func (m *DefaultManager) List() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Info
	for point, items := range m.handlers {
		for _, reg := range items {
			out = append(out, Info{Name: reg.name, Point: point, Priority: reg.priority, Detail: reg.detail})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Point == out[j].Point {
			return out[i].Name < out[j].Name
		}
		return out[i].Point < out[j].Point
	})
	return out
}

func (m *DefaultManager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers = map[Point][]registration{}
	m.next = 0
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

func (m *DefaultManager) logHook(ctx context.Context, mode string, reg registration, before, after Event, err error) {
	logger := m.loggerForLog()
	if logger == nil {
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
		logger.WarnContext(ctx, "hook error", attrs...)
		return
	}
	if logger.Enabled(ctx, slog.LevelDebug) {
		attrs = append(attrs,
			"session_id", after.Session.ID,
			"provider", after.LLM.Provider,
			"model", after.LLM.Model,
			"tool", after.Tool.Name,
			"before_text", trimLogText(eventText(before)),
			"after_text", trimLogText(eventText(after)),
			"raw_text", trimLogText(after.LLM.RawText),
		)
		logger.DebugContext(ctx, "hook triggered", attrs...)
	} else {
		logger.InfoContext(ctx, "hook triggered", attrs...)
	}
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
	if event.ErrorInfo == nil && event.Error != nil {
		event.ErrorInfo = &ErrorPayload{Message: event.Error.Error()}
	}
	return event
}

func EventErrorMessage(event Event) string {
	if event.ErrorInfo != nil {
		return event.ErrorInfo.Message
	}
	if event.Error != nil {
		return event.Error.Error()
	}
	return ""
}

func MessageInputText(event Event) string {
	if strings.TrimSpace(event.Message.InputText) != "" {
		return strings.TrimSpace(event.Message.InputText)
	}
	return llm.SegmentsTextOnly(event.Message.Segments)
}
