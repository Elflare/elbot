package hook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"elbot/internal/delivery"
	"elbot/internal/llm"
)

// Registration describes one explicitly matched hook handler.
type Registration struct {
	Point         Point
	Priority      int
	PluginID      string
	Name          string
	Description   string
	Match         Match
	Block         BlockPolicy
	Handler       Handler
	Detail        string
	RequireWakeup *bool
}

// Info describes a registered hook for inspection commands.
type Info struct {
	PluginID    string
	Name        string
	Description string
	Point       Point
	Priority    int
	Detail      string
}

type ReloadReport struct {
	Notices []string
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
}

// NoopManager is used when no hooks are configured.
type NoopManager struct{}

func (NoopManager) Register(Registration) error { return nil }

func (NoopManager) Run(_ context.Context, event Event) (Event, error) {
	return prepareEvent(event), nil
}

func (NoopManager) Notify(context.Context, Event) error { return nil }

func (NoopManager) List() []Info { return nil }

type DefaultManager struct {
	mu       sync.RWMutex
	next     int
	handlers map[Point][]registration
	logger   *slog.Logger
	wakeup   WakeupFunc
	observer Observer
}

type WakeupFunc func(context.Context, Event) bool

// ObserverInfo describes one matched hook handler execution.
type ObserverInfo struct {
	Point    Point
	Name     string
	Priority int
	Mode     string
}

// Observer can wrap a matched hook handler execution with extra lifecycle state.
type Observer func(context.Context, Event, ObserverInfo) (context.Context, func())

type registration struct {
	priority      int
	order         int
	pluginID      string
	name          string
	description   string
	match         Match
	block         BlockPolicy
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

func (m *DefaultManager) SetObserver(fn Observer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observer = fn
}

func (m *DefaultManager) Register(reg Registration) error {
	if err := validateRegistration(reg); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	m.handlers[reg.Point] = append(m.handlers[reg.Point], registration{priority: reg.Priority, order: m.next, pluginID: strings.TrimSpace(reg.PluginID), name: reg.Name, description: strings.TrimSpace(reg.Description), match: reg.Match, block: reg.Block, handler: reg.Handler, detail: reg.Detail, requireWakeup: registrationRequireWakeup(reg)})
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
		if reg.block.Blocks(event) {
			continue
		}
		if m.skipForWakeup(ctx, event, reg) {
			continue
		}
		matchResult := reg.match.MatchEvent(event)
		if !matchResult.OK {
			continue
		}
		event.Metadata["match"] = matchResult.Context
		before := event
		runCtx, done := m.observe(ctx, event, reg, "run")
		updated, err := func() (Event, error) {
			defer done()
			return reg.handler.HandleHook(runCtx, event)
		}()
		delete(event.Metadata, "match")
		if err != nil {
			m.logHook(runCtx, "run", reg, before, before, err)
			return event, wrapHookError(reg, err)
		}
		updated = markHookOutputs(updated, len(before.Outputs), reg, "run")
		event = prepareEvent(updated)
		m.logHook(runCtx, "run", reg, before, event, nil)
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
		if reg.block.Blocks(event) {
			continue
		}
		if m.skipForWakeup(ctx, event, reg) {
			continue
		}
		matchResult := reg.match.MatchEvent(event)
		if !matchResult.OK {
			continue
		}
		event.Metadata["match"] = matchResult.Context
		before := event
		runCtx, done := m.observe(ctx, event, reg, "notify")
		updated, err := func() (Event, error) {
			defer done()
			return reg.handler.HandleHook(runCtx, event)
		}()
		delete(event.Metadata, "match")
		if err != nil {
			m.logHook(runCtx, "notify", reg, before, before, err)
			joined = errors.Join(joined, wrapHookError(reg, err))
			continue
		}
		_ = markHookOutputs(updated, len(before.Outputs), reg, "notify")
		m.logHook(runCtx, "notify", reg, before, before, nil)
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

func (m *DefaultManager) observe(ctx context.Context, event Event, reg registration, mode string) (context.Context, func()) {
	observer := m.observerForRun()
	if observer == nil {
		return ctx, func() {}
	}
	runCtx, done := observer(ctx, event, ObserverInfo{Point: event.Point, Name: reg.name, Priority: reg.priority, Mode: mode})
	if runCtx == nil {
		runCtx = ctx
	}
	if done == nil {
		done = func() {}
	}
	return runCtx, done
}

func (m *DefaultManager) observerForRun() Observer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.observer
}

func (m *DefaultManager) List() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Info
	for point, items := range m.handlers {
		for _, reg := range items {
			out = append(out, Info{PluginID: reg.pluginID, Name: reg.name, Description: reg.description, Point: point, Priority: reg.priority, Detail: reg.detail})
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

// Replace atomically replaces registered handlers with a snapshot from next.
// Runtime integrations such as logging, wakeup checks, and observers remain
// attached to the receiving manager.
func (m *DefaultManager) Replace(next *DefaultManager) {
	if m == nil || next == nil || m == next {
		return
	}
	next.mu.RLock()
	handlers := make(map[Point][]registration, len(next.handlers))
	for point, items := range next.handlers {
		handlers[point] = append([]registration(nil), items...)
	}
	order := next.next
	next.mu.RUnlock()

	m.mu.Lock()
	m.handlers = handlers
	m.next = order
	m.mu.Unlock()
}

// ReplacePlugin atomically replaces only registrations owned by pluginID.
func (m *DefaultManager) ReplacePlugin(pluginID string, next *DefaultManager) {
	if m == nil || next == nil || m == next {
		return
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return
	}
	next.mu.RLock()
	replacements := make(map[Point][]registration)
	for point, items := range next.handlers {
		for _, reg := range items {
			if reg.pluginID == pluginID {
				replacements[point] = append(replacements[point], reg)
			}
		}
	}
	next.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	points := make(map[Point]struct{}, len(m.handlers)+len(replacements))
	for point := range m.handlers {
		points[point] = struct{}{}
	}
	for point := range replacements {
		points[point] = struct{}{}
	}
	for point := range points {
		kept := make([]registration, 0, len(m.handlers[point])+len(replacements[point]))
		insertAt := -1
		for _, reg := range m.handlers[point] {
			if reg.pluginID == pluginID {
				if insertAt == -1 {
					insertAt = len(kept)
				}
				continue
			}
			kept = append(kept, reg)
		}
		if insertAt == -1 {
			insertAt = len(kept)
		}
		kept = append(kept, make([]registration, len(replacements[point]))...)
		copy(kept[insertAt+len(replacements[point]):], kept[insertAt:len(kept)-len(replacements[point])])
		copy(kept[insertAt:], replacements[point])
		for i := range kept {
			m.next++
			kept[i].order = m.next
		}
		if len(kept) == 0 {
			delete(m.handlers, point)
			continue
		}
		sort.SliceStable(kept, func(i, j int) bool {
			if kept[i].priority == kept[j].priority {
				return kept[i].order < kept[j].order
			}
			return kept[i].priority < kept[j].priority
		})
		m.handlers[point] = kept
	}
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
		if errors.Is(err, context.Canceled) {
			logger.InfoContext(ctx, "hook canceled", attrs...)
			return
		}
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
			"source_text", trimLogText(after.LLM.SourceText),
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
