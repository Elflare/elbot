// 组织续接、普通规则与错误钩子，管理规则注册、活动执行和替换。

package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"

	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/hook/share"
	"elbot/internal/llm"
)

type Manager struct {
	mu       sync.RWMutex
	next     int
	activeID int
	handlers map[share.Point][]registration
	// route is injected at construction; runtime owns continuation state and execution.
	route  func(context.Context, share.Event, func(context.Context, string) (context.Context, func(), error)) (share.Event, bool, error)
	Log    func(context.Context, globalevents.LogRecord) // Publishes complete source-owned records.
	active map[string]activeHook
}

type activeHook struct {
	name     string
	pluginID string
	cancel   context.CancelFunc
}

type registration struct {
	priority    int
	order       int
	pluginID    string
	name        string
	description string
	match       share.Match
	block       share.BlockPolicy
	handler     share.Handler
	detail      string
	wakeup      share.WakeupPolicy
}

func NewManager(Log func(context.Context, globalevents.LogRecord), route func(context.Context, share.Event, func(context.Context, string) (context.Context, func(), error)) (share.Event, bool, error)) *Manager {
	return &Manager{handlers: map[share.Point][]registration{}, active: map[string]activeHook{}, Log: Log, route: route}
}

func (m *Manager) Register(reg share.Registration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	m.handlers[reg.Point] = append(m.handlers[reg.Point], registration{priority: reg.Priority, order: m.next, pluginID: strings.TrimSpace(reg.PluginID), name: reg.Name, description: strings.TrimSpace(reg.Description), match: reg.Match, block: reg.Block, handler: reg.Handler, detail: reg.Detail, wakeup: reg.Wakeup})
	sort.SliceStable(m.handlers[reg.Point], func(i, j int) bool {
		left := m.handlers[reg.Point][i]
		right := m.handlers[reg.Point][j]
		if left.priority == right.priority {
			return left.order < right.order
		}
		return left.priority < right.priority
	})
}

// Run first tries an inbound continuation, then runs ordinary rules unless stopped.
// On failure it returns the entry snapshot and notifies error hooks once.
func (m *Manager) Run(ctx context.Context, event share.Event, options CallOptions) (share.Event, error) {
	before := share.SnapshotCalls(event)
	if event.Point == share.PointPlatformMessageReceived && m.route != nil {
		updated, routed, err := m.route(ctx, event, options.Observe)
		if err != nil {
			m.notifyError(ctx, before, options, err)
			return before, err
		}
		event = updated
		if routed && event.Control.StopPropagation {
			return event, nil
		}
	}
	updated, err := m.run(ctx, event, options)
	if err != nil {
		m.notifyError(ctx, before, options, err)
		return before, err
	}
	return updated, nil
}

func (m *Manager) Notify(ctx context.Context, event share.Event, options CallOptions) error {
	err := m.notify(ctx, event, options)
	if err != nil {
		m.notifyError(ctx, event, options, err)
	}
	return err
}

func (m *Manager) notifyError(ctx context.Context, event share.Event, options CallOptions, err error) {
	if ctx.Err() != nil || event.Point == share.PointErrorOccurred {
		return
	}
	event.Point, event.Error, event.ErrorInfo = share.PointErrorOccurred, err, nil
	// Do not recurse into error reporting or replace the original failure.
	_ = m.notify(ctx, event, options)
}

func (m *Manager) run(ctx context.Context, event share.Event, options CallOptions) (share.Event, error) {
	event = share.PrepareEvent(event)
	for _, reg := range m.handlersFor(event.Point) {
		if reg.block.Blocks(event) {
			continue
		}
		if skipForWakeup(options.Woken, reg) {
			continue
		}
		matchResult := reg.match.MatchEvent(event)
		if !matchResult.OK {
			continue
		}
		event.Metadata["match"] = matchResult.Context
		before := share.SnapshotCalls(event)
		runCtx, updated, err := m.invoke(ctx, reg, event, options)
		delete(event.Metadata, "match")

		if err != nil {
			m.logHook(runCtx, "run", reg, before, before, err)
			return before, wrapHookError(reg, err)
		}
		markHookOutputs(updated, len(before.Outputs), reg, "run")
		event = share.PrepareEvent(updated)
		m.logHook(runCtx, "run", reg, before, event, err)

		if event.Control.StopPropagation {
			break
		}
	}
	return event, nil
}

func (m *Manager) notify(ctx context.Context, event share.Event, options CallOptions) error {
	event = share.PrepareEvent(event)
	var joined error
	for _, reg := range m.handlersFor(event.Point) {
		if reg.block.Blocks(event) {
			continue
		}
		if skipForWakeup(options.Woken, reg) {
			continue
		}
		matchResult := reg.match.MatchEvent(event)
		if !matchResult.OK {
			continue
		}
		event.Metadata["match"] = matchResult.Context
		before := event
		runCtx, updated, err := m.invoke(ctx, reg, event, options)
		delete(event.Metadata, "match")
		if err != nil {
			m.logHook(runCtx, "notify", reg, before, before, err)
			joined = errors.Join(joined, wrapHookError(reg, err))
			continue
		}
		markHookOutputs(updated, len(before.Outputs), reg, "notify")
		m.logHook(runCtx, "notify", reg, before, before, err)
		if updated.Control.StopPropagation {
			break
		}
	}
	return joined
}

func (m *Manager) handlersFor(point share.Point) []registration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := m.handlers[point]
	out := make([]registration, len(items))
	copy(out, items)
	return out
}

func skipForWakeup(woken bool, reg registration) bool {
	switch reg.wakeup {
	case share.WakeupAny:
		return false
	case share.WakeupForbidden:
		return woken
	default:
		return !woken
	}
}

func (m *Manager) invoke(ctx context.Context, reg registration, event share.Event, options CallOptions) (context.Context, share.Event, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	activeID := m.startActive(reg, cancel)
	defer m.finishActive(activeID)
	runCtx, done, err := observe(ctx, reg, options)
	if err != nil {
		return runCtx, event, err
	}
	defer done()
	updated, err := reg.handler.HandleHook(runCtx, event, options.ReadOnlyCalls)
	return runCtx, updated, err
}

func observe(ctx context.Context, reg registration, options CallOptions) (context.Context, func(), error) {
	if options.Observe == nil {
		return ctx, func() {}, nil
	}
	return options.Observe(ctx, reg.name)
}

func (m *Manager) startActive(reg registration, cancel context.CancelFunc) string {
	m.mu.Lock()
	m.activeID++
	id := fmt.Sprintf("%s:%d", reg.name, m.activeID)
	m.active[id] = activeHook{name: reg.name, pluginID: reg.pluginID, cancel: cancel}
	m.mu.Unlock()
	return id
}

func (m *Manager) finishActive(id string) {
	m.mu.Lock()
	delete(m.active, id)
	m.mu.Unlock()
}

// Stop cancels active normal Hook executions matching a rule name or plugin ID.
func (m *Manager) Stop(id string) int {
	id = strings.TrimSpace(id)
	if id == "" {
		return 0
	}
	m.mu.RLock()
	cancels := make([]context.CancelFunc, 0)
	for _, active := range m.active {
		if active.name == id || active.pluginID == id {
			cancels = append(cancels, active.cancel)
		}
	}
	m.mu.RUnlock()
	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
}

// Matches reports whether id names a normal rule or plugin.
func (m *Manager) Matches(id string) bool {
	id = strings.TrimSpace(id)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, items := range m.handlers {
		for _, reg := range items {
			if reg.name == id || (reg.pluginID != "" && reg.pluginID == id) {
				return true
			}
		}
	}
	return false
}
func (m *Manager) List() []share.Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []share.Info
	activeCounts := make(map[string]int, len(m.active))
	for _, running := range m.active {
		activeCounts[running.name]++
	}
	for point, items := range m.handlers {
		for _, reg := range items {
			out = append(out, share.Info{PluginID: reg.pluginID, Name: reg.name, Description: reg.description, Point: point, Priority: reg.priority, Detail: reg.detail, Active: activeCounts[reg.name]})
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
// The receiving manager retains its continuation and log publishing capabilities.
func (m *Manager) Replace(next *Manager) {
	next.mu.RLock()
	handlers := make(map[share.Point][]registration, len(next.handlers))
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
func (m *Manager) ReplacePlugin(pluginID string, next *Manager) {
	pluginID = strings.TrimSpace(pluginID)
	next.mu.RLock()
	replacements := make(map[share.Point][]registration)
	order := map[string]int{}
	for point, items := range next.handlers {
		for _, reg := range items {
			if previous, ok := order[reg.pluginID]; !ok || reg.order < previous {
				order[reg.pluginID] = reg.order
			}
			if reg.pluginID == pluginID {
				replacements[point] = append(replacements[point], reg)
			}
		}
	}
	next.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	points := make(map[share.Point]struct{}, len(m.handlers)+len(replacements))
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
		kept = slices.Insert(kept, insertAt, replacements[point]...)
		for _, reg := range kept {
			m.next = max(m.next, reg.order)
		}
		if len(kept) == 0 {
			delete(m.handlers, point)
			continue
		}
		sort.SliceStable(kept, func(i, j int) bool {
			if kept[i].priority == kept[j].priority {
				if kept[i].pluginID != kept[j].pluginID {
					left, leftOK := order[kept[i].pluginID]
					right, rightOK := order[kept[j].pluginID]
					if leftOK && rightOK {
						return left < right
					}
					if leftOK != rightOK {
						return leftOK
					}
				}
				return kept[i].order < kept[j].order
			}
			return kept[i].priority < kept[j].priority
		})
		m.handlers[point] = kept
	}
}

func wrapHookError(reg registration, err error) error {
	return fmt.Errorf("hook %s: %w", reg.name, err)
}

func markHookOutputs(event share.Event, start int, reg registration, mode string) {
	for i := start; i < len(event.Outputs); i++ {
		info := &event.Outputs[i].Delivery
		if info.HookPoint == "" {
			info.HookPoint = string(event.Point)
		}
		if info.HookName == "" {
			info.HookName = reg.name
		}
		if info.HookMode == "" {
			info.HookMode = mode
		}
	}
}

func (m *Manager) logHook(ctx context.Context, mode string, reg registration, before, after share.Event, err error) {
	level, name, status, detail := slog.LevelInfo, "hook_triggered", globalevents.ResultSucceeded, ""
	if err != nil {
		level, name, status, detail = slog.LevelWarn, "hook_error", globalevents.ResultFailed, err.Error()
		if globalevents.LogStatus(err) == globalevents.ResultCanceled {
			level, name, status = slog.LevelInfo, "hook_canceled", globalevents.ResultCanceled
		}
	}
	beforeText := eventText(before)
	content := map[string]any{
		"point": before.Point, "hook": reg.name, "priority": reg.priority, "order": reg.order, "mode": mode,
		"detail":      share.TruncateLogContent(detail, 1024),
		"before_text": share.TruncateLogContent(beforeText, 1024),
		"after_text":  share.TruncateLogContent(eventText(after), 1024),
	}
	if reg.pluginID != "" {
		content["plugin"] = reg.pluginID
	}
	if after.LLM.Provider != "" {
		content["provider"] = after.LLM.Provider
	}
	if after.LLM.Model != "" {
		content["model"] = after.LLM.Model
	}
	if after.Tool.Name != "" {
		content["tool"] = after.Tool.Name
	}
	if after.LLM.SourceText != "" {
		if after.LLM.SourceText == beforeText {
			content["source_text"] = "同before_text"
		} else {
			content["source_text"] = share.TruncateLogContent(after.LLM.SourceText, 1024)
		}
	}
	data, _ := json.Marshal(content)
	record := globalevents.LogRecord{
		Category: globalevents.LogRuntime, Level: level, EventName: name, Module: "hook",
		ResultStatus: status, Type: globalevents.ContentJSON, Content: string(data),
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	record.Identity = globalevents.LogIdentityFromExecution(facts)
	if before.Session.ID != "" {
		record.Identity.SessionID = before.Session.ID
	}
	m.Log(ctx, record)
}

func eventText(event share.Event) string {
	if event.LLM.Text != "" {
		return event.LLM.Text
	}
	text := llm.SegmentsRawContentText(event.Message.Segments)
	switch {
	case text != "":
		return text
	case event.Tool.Result != "":
		return event.Tool.Result
	case event.Tool.Arguments != "":
		return event.Tool.Arguments
	default:
		return ""
	}
}
