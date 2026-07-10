// Package runtime runs stateful hook.v2 processes. It deliberately lives under
// the Hook subsystem: a persistent hook is still a Hook, not another plugin
// dispatch path.
package runtime

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool"
)

const protocolVersion = "hook.v2"

const maxToolCallsPerInvocation = 32

const dispatchedMetadataKey = "hook.runtime.dispatched"

type Status string

const (
	StatusStarting Status = "starting"
	StatusReady    Status = "ready"
	StatusRunning  Status = "running"
	StatusDegraded Status = "degraded"
	StatusStopping Status = "stopping"
	StatusStopped  Status = "stopped"
	StatusFailed   Status = "failed"
)

type RestartConfig struct {
	Strategy            string `toml:"strategy"`
	InitialDelaySeconds int    `toml:"initial_delay_seconds"`
	MaxDelaySeconds     int    `toml:"max_delay_seconds"`
}

type ToolsConfig struct {
	Allow           []string `toml:"allow"`
	BackgroundAllow []string `toml:"background_allow"`
}

// Config is decoded from [plugin.runtime]. All stateful lifecycle values are
// explicit so a configuration never inherits surprising restart behaviour.
type Config struct {
	Stateful               bool          `toml:"stateful"`
	Command                string        `toml:"command"`
	Cwd                    string        `toml:"cwd"`
	StartupTimeoutSeconds  int           `toml:"startup_timeout_seconds"`
	ShutdownTimeoutSeconds int           `toml:"shutdown_timeout_seconds"`
	EventTimeoutSeconds    int           `toml:"event_timeout_seconds"`
	MaxWaitSeconds         int           `toml:"max_wait_seconds"`
	Restart                RestartConfig `toml:"restart"`
	Tools                  ToolsConfig   `toml:"tools"`

	ID          string `toml:"-"`
	Description string `toml:"-"`
	Dir         string `toml:"-"`
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("hook id is required")
	}
	if !validID(c.ID) {
		return fmt.Errorf("hook id %q must contain only lowercase letters, digits, '-' or '_'", c.ID)
	}
	if !c.Stateful {
		return fmt.Errorf("[plugin.runtime] requires stateful = true")
	}
	if strings.TrimSpace(c.Command) == "" {
		return fmt.Errorf("runtime command is required")
	}
	if strings.TrimSpace(c.Cwd) == "" {
		return fmt.Errorf("runtime cwd is required")
	}
	if c.StartupTimeoutSeconds <= 0 || c.ShutdownTimeoutSeconds <= 0 || c.EventTimeoutSeconds <= 0 || c.MaxWaitSeconds <= 0 {
		return fmt.Errorf("runtime startup_timeout_seconds, shutdown_timeout_seconds, event_timeout_seconds and max_wait_seconds must be positive")
	}
	strategy := strings.TrimSpace(c.Restart.Strategy)
	if strategy != "never" && strategy != "on_failure" && strategy != "always" {
		return fmt.Errorf("runtime restart.strategy must be never, on_failure or always")
	}
	if c.Restart.InitialDelaySeconds <= 0 || c.Restart.MaxDelaySeconds <= 0 || c.Restart.InitialDelaySeconds > c.Restart.MaxDelaySeconds {
		return fmt.Errorf("runtime restart delays must be positive and initial_delay_seconds cannot exceed max_delay_seconds")
	}
	if strings.TrimSpace(c.Dir) == "" {
		return fmt.Errorf("runtime plugin directory is required")
	}
	return nil
}

func validID(value string) bool {
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return value != ""
}

type Info struct {
	ID          string
	Description string
	Status      Status
	Detail      string
}

type Options struct {
	Registry  *tool.Registry
	Logger    *slog.Logger
	Audit     func(event string, attrs ...any)
	Send      func(context.Context, delivery.Target, delivery.Output) (delivery.Receipt, error)
	SharedDir string
}

type Manager struct {
	mu      sync.RWMutex
	opts    Options
	workers map[string]*worker
	routes  map[routeKey]lease
	running map[routeKey]invocation
	tokens  map[string]toolContext
	shared  *SharedState
}

func NewManager(opts Options) *Manager {
	if strings.TrimSpace(opts.SharedDir) != "" {
		_ = os.MkdirAll(opts.SharedDir, 0o755)
	}
	return &Manager{
		opts:    opts,
		workers: map[string]*worker{},
		routes:  map[routeKey]lease{},
		running: map[routeKey]invocation{},
		tokens:  map[string]toolContext{},
		shared:  NewSharedState(),
	}
}

func (m *Manager) SharedState() *SharedState {
	if m == nil {
		return nil
	}
	return m.shared
}

// Apply reconciles configured persistent hooks. It is safe to call on startup
// and from /hooks reload; unchanged entries are restarted so config changes are
// deterministic rather than partially live-patched.
func (m *Manager) Apply(configs []Config) error {
	if m == nil {
		return nil
	}
	next := map[string]Config{}
	for _, config := range configs {
		if err := m.validateConfig(config); err != nil {
			return err
		}
		if _, exists := next[config.ID]; exists {
			return fmt.Errorf("duplicate stateful hook %q", config.ID)
		}
		next[config.ID] = config
	}
	m.mu.Lock()
	previous := m.workers
	m.workers = map[string]*worker{}
	for id := range previous {
		m.clearRoutesLocked(id)
	}
	m.mu.Unlock()
	for _, old := range previous {
		old.stop(context.Background())
	}
	workers := make([]*worker, 0, len(next))
	m.mu.Lock()
	for id, config := range next {
		w := newWorker(m, config)
		m.workers[id] = w
		workers = append(workers, w)
	}
	m.mu.Unlock()
	for _, w := range workers {
		go w.run()
	}
	return nil
}

func (m *Manager) validateConfig(config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, name := range config.Tools.Allow {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return fmt.Errorf("runtime tools.allow contains an empty or duplicate tool name")
		}
		seen[name] = true
		if m.opts.Registry == nil {
			return fmt.Errorf("runtime tool registry is not configured")
		}
		if _, ok := m.opts.Registry.Get(name); !ok {
			return fmt.Errorf("runtime tools.allow references unknown tool %q", name)
		}
	}
	for _, name := range config.Tools.BackgroundAllow {
		name = strings.TrimSpace(name)
		if name == "" || !seen[name] {
			return fmt.Errorf("runtime tools.background_allow tool %q must also appear in tools.allow", name)
		}
	}
	return nil
}

func (m *Manager) Close(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.RLock()
	workers := make([]*worker, 0, len(m.workers))
	for _, w := range m.workers {
		workers = append(workers, w)
	}
	m.mu.RUnlock()
	for _, w := range workers {
		w.stop(ctx)
	}
}

func (m *Manager) List() []Info {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Info, 0, len(m.workers))
	for _, worker := range m.workers {
		items = append(items, worker.info())
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (m *Manager) Start(id string) error {
	w := m.worker(id)
	if w == nil {
		return fmt.Errorf("stateful hook %q not found", id)
	}
	w.start()
	return nil
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	w := m.worker(id)
	if w == nil {
		return fmt.Errorf("stateful hook %q not found", id)
	}
	w.stop(ctx)
	m.mu.Lock()
	m.clearRoutesLocked(id)
	m.mu.Unlock()
	return nil
}

func (m *Manager) Restart(ctx context.Context, id string) error {
	if err := m.Stop(ctx, id); err != nil {
		return err
	}
	return m.Start(id)
}

func (m *Manager) worker(id string) *worker {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.workers[strings.TrimSpace(id)]
}

func (m *Manager) Handle(ctx context.Context, id string, event hook.Event) (hook.Event, error) {
	if m.hasLease(event) {
		return event, nil
	}
	w := m.worker(id)
	if w == nil {
		return event, fmt.Errorf("stateful hook %q is not configured", id)
	}
	updated, err := w.handle(ctx, event, false)
	if err == nil {
		if updated.Metadata == nil {
			updated.Metadata = map[string]any{}
		}
		updated.Metadata[dispatchedMetadataKey] = true
	}
	return updated, err
}

func (m *Manager) hasLease(event hook.Event) bool {
	if m == nil {
		return false
	}
	key := routeKeyFor(event)
	m.mu.Lock()
	defer m.mu.Unlock()
	lease, ok := m.routes[key]
	if ok && time.Now().After(lease.ExpiresAt) {
		delete(m.routes, key)
		return false
	}
	return ok
}

// Route dispatches a captured continuation before Agent wakeup, commands and
// LLM processing. The caller owns normal Hook execution before calling Route.
func (m *Manager) Route(ctx context.Context, event hook.Event) (bool, []delivery.Output, error) {
	if m == nil {
		return false, nil, nil
	}
	if dispatched, _ := event.Metadata[dispatchedMetadataKey].(bool); dispatched {
		return false, nil, nil
	}
	key := routeKeyFor(event)
	m.mu.Lock()
	lease, ok := m.routes[key]
	if ok && time.Now().After(lease.ExpiresAt) {
		delete(m.routes, key)
		ok = false
	}
	m.mu.Unlock()
	if !ok {
		return false, nil, nil
	}
	w := m.worker(lease.HookID)
	if w == nil {
		m.mu.Lock()
		delete(m.routes, key)
		m.mu.Unlock()
		return false, nil, nil
	}
	updated, err := w.handle(ctx, event, true)
	return true, appendedOutputs(event.Outputs, updated.Outputs), err
}

func (m *Manager) Cancel(event hook.Event) bool {
	if m == nil {
		return false
	}
	key := routeKeyFor(event)
	m.mu.Lock()
	lease, ok := m.routes[key]
	if ok {
		delete(m.routes, key)
	}
	running, runningOK := m.running[key]
	m.mu.Unlock()
	if !ok && !runningOK {
		return false
	}
	if runningOK && running.Cancel != nil {
		running.Cancel()
	}
	hookID := lease.HookID
	conversationID := lease.ConversationID
	if hookID == "" && runningOK {
		hookID = running.HookID
	}
	if w := m.worker(hookID); w != nil {
		w.notifyCancel(conversationID)
	}
	return true
}

func appendedOutputs(before, after []delivery.Output) []delivery.Output {
	if len(after) <= len(before) {
		return nil
	}
	return append([]delivery.Output(nil), after[len(before):]...)
}

type routeKey struct {
	Platform string
	ScopeID  string
	ActorID  string
}

func routeKeyFor(event hook.Event) routeKey {
	return routeKey{Platform: event.Platform.Name, ScopeID: event.Platform.ScopeID, ActorID: event.Actor.ID}
}

type lease struct {
	HookID         string
	ConversationID string
	ExpiresAt      time.Time
}

type invocation struct {
	HookID string
	Cancel context.CancelFunc
}

func (m *Manager) beginInvocation(key routeKey, hookID string, cancel context.CancelFunc) {
	m.mu.Lock()
	m.running[key] = invocation{HookID: hookID, Cancel: cancel}
	m.mu.Unlock()
}

func (m *Manager) endInvocation(key routeKey, cancel context.CancelFunc) {
	_ = cancel
	m.mu.Lock()
	delete(m.running, key)
	m.mu.Unlock()
}

func (m *Manager) setLease(id string, event hook.Event, result eventResult) error {
	if result.Status != "waiting" {
		return nil
	}
	if strings.TrimSpace(result.ConversationID) == "" {
		return fmt.Errorf("hook waiting response requires conversation_id")
	}
	if result.ExpiresAt.IsZero() || !result.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("hook waiting response requires a future expires_at")
	}
	w := m.worker(id)
	if w == nil {
		return fmt.Errorf("stateful hook %q is no longer configured", id)
	}
	max := time.Duration(w.config.MaxWaitSeconds) * time.Second
	if result.ExpiresAt.After(time.Now().Add(max)) {
		return fmt.Errorf("hook waiting response exceeds max_wait_seconds")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := routeKeyFor(event)
	m.routes[key] = lease{HookID: id, ConversationID: result.ConversationID, ExpiresAt: result.ExpiresAt}
	return nil
}

func (m *Manager) clearRoutesLocked(id string) {
	for key, lease := range m.routes {
		if lease.HookID == id {
			delete(m.routes, key)
		}
	}
	for key, running := range m.running {
		if running.HookID == id {
			if running.Cancel != nil {
				running.Cancel()
			}
			delete(m.running, key)
		}
	}
}

type toolContext struct {
	HookID    string
	Event     hook.Event
	Context   context.Context
	ExpiresAt time.Time
	Calls     int
}

func (m *Manager) putToolContext(id string, ctx context.Context, event hook.Event, ttl time.Duration) string {
	token := randomID("ctx")
	m.mu.Lock()
	m.tokens[token] = toolContext{HookID: id, Event: event, Context: ctx, ExpiresAt: time.Now().Add(ttl)}
	m.mu.Unlock()
	return token
}

func (m *Manager) takeToolContext(token, hookID string) (toolContext, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.tokens[token]
	if !ok || value.HookID != hookID || time.Now().After(value.ExpiresAt) || value.Calls >= maxToolCallsPerInvocation {
		delete(m.tokens, token)
		return toolContext{}, false
	}
	value.Calls++
	m.tokens[token] = value
	return value, true
}

func randomID(prefix string) string {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return fmt.Sprintf("%s:%d", prefix, time.Now().UnixNano())
	}
	return prefix + ":" + hex.EncodeToString(data[:])
}

type frame struct {
	Type   string          `json:"type"`
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	OK     *bool           `json:"ok,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type worker struct {
	manager *Manager
	config  Config

	mu           sync.Mutex
	status       Status
	detail       string
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	pending      map[string]chan frame
	stopping     bool
	manualStop   bool
	restartDelay time.Duration
	done         chan struct{}
	writeMu      sync.Mutex
	routeMu      sync.Mutex
	routeLocks   map[routeKey]chan struct{}
}

func newWorker(manager *Manager, config Config) *worker {
	return &worker{manager: manager, config: config, status: StatusStopped, pending: map[string]chan frame{}, routeLocks: map[routeKey]chan struct{}{}}
}

func (w *worker) start() {
	w.mu.Lock()
	w.manualStop = false
	w.mu.Unlock()
	go w.run()
}

func (w *worker) info() Info {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Info{ID: w.config.ID, Description: w.config.Description, Status: w.status, Detail: w.detail}
}

func (w *worker) setStatus(status Status, detail string) {
	w.mu.Lock()
	w.status = status
	w.detail = strings.TrimSpace(detail)
	w.mu.Unlock()
}

func (w *worker) run() {
	w.mu.Lock()
	if w.cmd != nil || w.stopping {
		w.mu.Unlock()
		return
	}
	w.status = StatusStarting
	w.detail = ""
	w.done = make(chan struct{})
	w.mu.Unlock()

	argv, err := splitCommand(w.config.Command)
	if err != nil {
		w.startFailed(err)
		return
	}
	cwd, err := resolveCwd(w.config.Dir, w.config.Cwd)
	if err != nil {
		w.startFailed(err)
		return
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		w.startFailed(err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		w.startFailed(err)
		return
	}
	cmd.Stderr = stderrLogger{logger: w.manager.opts.Logger, hookID: w.config.ID}
	if err := cmd.Start(); err != nil {
		w.startFailed(err)
		return
	}
	w.mu.Lock()
	w.cmd = cmd
	w.stdin = stdin
	w.pending = map[string]chan frame{}
	w.mu.Unlock()
	go w.readLoop(stdout)
	go w.waitLoop(cmd)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(w.config.StartupTimeoutSeconds)*time.Second)
	defer cancel()
	init := systemInit{Version: protocolVersion, Hook: systemHook{ID: w.config.ID, Description: w.config.Description, Dir: w.config.Dir, Cwd: cwd, SharedDir: w.manager.opts.SharedDir}, Tools: w.schemas()}
	if _, err := w.request(ctx, "system.init", init); err != nil {
		w.setStatus(StatusFailed, err.Error())
		w.kill()
		return
	}
	w.mu.Lock()
	w.restartDelay = time.Duration(w.config.Restart.InitialDelaySeconds) * time.Second
	w.mu.Unlock()
	w.setStatus(StatusReady, "")
}

func (w *worker) startFailed(err error) {
	w.setStatus(StatusFailed, err.Error())
	if w.shouldRestart() {
		go w.restartLater()
	}
}

func (w *worker) waitLoop(cmd *exec.Cmd) {
	err := cmd.Wait()
	w.mu.Lock()
	stopping := w.stopping
	w.cmd = nil
	w.stdin = nil
	for id, pending := range w.pending {
		delete(w.pending, id)
		close(pending)
	}
	if w.done != nil {
		close(w.done)
	}
	w.mu.Unlock()
	w.mu.Lock()
	manualStop := w.manualStop
	w.mu.Unlock()
	if !stopping && !manualStop {
		detail := "hook process exited"
		if err != nil {
			detail = err.Error()
		}
		w.setStatus(StatusDegraded, detail)
		w.manager.mu.Lock()
		w.manager.clearRoutesLocked(w.config.ID)
		w.manager.mu.Unlock()
		if w.shouldRestart() {
			go w.restartLater()
		}
		return
	}
	w.setStatus(StatusStopped, "")
}

func (w *worker) shouldRestart() bool {
	return w.config.Restart.Strategy == "on_failure" || w.config.Restart.Strategy == "always"
}

func (w *worker) restartLater() {
	w.mu.Lock()
	delay := w.restartDelay
	if delay <= 0 {
		delay = time.Duration(w.config.Restart.InitialDelaySeconds) * time.Second
	}
	next := delay * 2
	maxDelay := time.Duration(w.config.Restart.MaxDelaySeconds) * time.Second
	if next > maxDelay {
		next = maxDelay
	}
	w.restartDelay = next
	w.mu.Unlock()
	if delay <= 0 {
		return
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	<-timer.C
	w.mu.Lock()
	stopping := w.stopping || w.manualStop
	w.mu.Unlock()
	if !stopping {
		w.run()
	}
}

func (w *worker) stop(ctx context.Context) {
	w.mu.Lock()
	if w.stopping {
		w.mu.Unlock()
		return
	}
	w.stopping = true
	w.manualStop = true
	cmd := w.cmd
	done := w.done
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.stopping = false
		w.mu.Unlock()
	}()
	if cmd == nil {
		w.setStatus(StatusStopped, "")
		return
	}
	w.setStatus(StatusStopping, "")
	shutdownCtx, cancel := context.WithTimeout(ctx, time.Duration(w.config.ShutdownTimeoutSeconds)*time.Second)
	_, _ = w.request(shutdownCtx, "system.shutdown", map[string]any{})
	cancel()
	select {
	case <-done:
	case <-time.After(time.Duration(w.config.ShutdownTimeoutSeconds) * time.Second):
		w.kill()
	}
}

func (w *worker) kill() {
	w.mu.Lock()
	cmd := w.cmd
	w.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (w *worker) handle(ctx context.Context, event hook.Event, continuation bool) (hook.Event, error) {
	key := routeKeyFor(event)
	unlock, err := w.lockRoute(ctx, key)
	if err != nil {
		return event, err
	}
	defer unlock()
	w.mu.Lock()
	status := w.status
	w.mu.Unlock()
	if status != StatusReady && status != StatusRunning {
		return event, fmt.Errorf("stateful hook %q is %s", w.config.ID, status)
	}
	w.setStatus(StatusRunning, "")
	defer func() {
		w.mu.Lock()
		if w.cmd != nil && w.status == StatusRunning {
			w.status = StatusReady
		}
		w.mu.Unlock()
	}()
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(w.config.EventTimeoutSeconds)*time.Second)
	w.manager.beginInvocation(key, w.config.ID, cancel)
	defer w.manager.endInvocation(key, cancel)
	defer cancel()
	token := w.manager.putToolContext(w.config.ID, requestCtx, event, time.Duration(w.config.EventTimeoutSeconds)*time.Second)
	params := eventHandle{Event: event, Match: eventMatch(event), Continuation: continuation, ToolContext: token}
	response, err := w.request(requestCtx, "event.handle", params)
	if err != nil {
		return event, err
	}
	var result eventResult
	if len(response) > 0 {
		if err := json.Unmarshal(response, &result); err != nil {
			return event, fmt.Errorf("decode hook event response: %w", err)
		}
	}
	switch result.Status {
	case "", "completed":
	case "waiting":
		if err := w.manager.setLease(w.config.ID, event, result); err != nil {
			return event, err
		}
	default:
		return event, fmt.Errorf("unsupported hook event status %q", result.Status)
	}
	outputs, err := w.outputs(result.Outputs)
	if err != nil {
		return event, err
	}
	event.Outputs = append(event.Outputs, outputs...)
	if result.Consume {
		event.Control.Consume = true
	}
	if result.StopPropagation {
		event.Control.StopPropagation = true
	}
	return event, nil
}

func (w *worker) lockRoute(ctx context.Context, key routeKey) (func(), error) {
	w.routeMu.Lock()
	lock := w.routeLocks[key]
	if lock == nil {
		lock = make(chan struct{}, 1)
		lock <- struct{}{}
		w.routeLocks[key] = lock
	}
	w.routeMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-lock:
		return func() { lock <- struct{}{} }, nil
	}
}

func (w *worker) notifyCancel(conversationID string) {
	params := map[string]string{}
	if strings.TrimSpace(conversationID) != "" {
		params["conversation_id"] = conversationID
	}
	_ = w.write(frame{Type: "event", Method: "event.cancel", Params: mustJSON(params)})
}

func (w *worker) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := randomID("host")
	response := make(chan frame, 1)
	w.mu.Lock()
	if w.stdin == nil {
		w.mu.Unlock()
		return nil, fmt.Errorf("hook process is not running")
	}
	w.pending[id] = response
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.pending, id)
		w.mu.Unlock()
	}()
	if err := w.write(frame{Type: "request", ID: id, Method: method, Params: mustJSON(params)}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value, ok := <-response:
		if !ok {
			return nil, fmt.Errorf("hook process exited while waiting for %s", method)
		}
		if value.OK == nil || !*value.OK {
			if value.Error == "" {
				value.Error = "hook request failed"
			}
			return nil, errors.New(value.Error)
		}
		return value.Result, nil
	}
}

func (w *worker) write(value frame) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	w.mu.Lock()
	stdin := w.stdin
	w.mu.Unlock()
	if stdin == nil {
		return fmt.Errorf("hook process stdin is closed")
	}
	if _, err := fmt.Fprintf(stdin, "%s\n", data); err != nil {
		return fmt.Errorf("write hook protocol frame: %w", err)
	}
	return nil
}

func (w *worker) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytesTrim(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var value frame
		if err := json.Unmarshal(line, &value); err != nil {
			w.setStatus(StatusDegraded, "invalid hook.v2 stdout frame: "+err.Error())
			w.kill()
			return
		}
		switch value.Type {
		case "response":
			w.deliverResponse(value)
		case "request":
			if !strings.HasPrefix(value.ID, "plugin:") || strings.TrimSpace(value.Method) == "" {
				w.setStatus(StatusDegraded, "invalid hook request frame")
				w.kill()
				return
			}
			go w.handlePluginRequest(value)
		case "event":
			w.handlePluginEvent(value)
		default:
			w.setStatus(StatusDegraded, "unsupported hook.v2 frame type "+value.Type)
			w.kill()
			return
		}
	}
	if err := scanner.Err(); err != nil {
		w.setStatus(StatusDegraded, "read hook stdout: "+err.Error())
	}
}

func bytesTrim(value []byte) []byte {
	return []byte(strings.TrimSpace(string(value)))
}

func (w *worker) deliverResponse(value frame) {
	if !strings.HasPrefix(value.ID, "host:") {
		w.setStatus(StatusDegraded, "response id must use host: prefix")
		w.kill()
		return
	}
	w.mu.Lock()
	pending := w.pending[value.ID]
	w.mu.Unlock()
	if pending != nil {
		pending <- value
	}
}

func (w *worker) handlePluginEvent(value frame) {
	if value.Method == "hook.log" && w.manager.opts.Logger != nil {
		w.manager.opts.Logger.Info("stateful hook event", "hook", w.config.ID, "params", string(value.Params))
	}
}

func (w *worker) handlePluginRequest(value frame) {
	result, err := w.pluginRequest(value)
	ok := err == nil
	response := frame{Type: "response", ID: value.ID, OK: &ok}
	if err != nil {
		response.Error = err.Error()
	} else {
		response.Result = mustJSON(result)
	}
	if writeErr := w.write(response); writeErr != nil && w.manager.opts.Logger != nil {
		w.manager.opts.Logger.Warn("write hook request response failed", "hook", w.config.ID, "method", value.Method, "error", writeErr)
	}
}

func (w *worker) pluginRequest(value frame) (any, error) {
	switch value.Method {
	case "tool.call":
		return w.callTool(value.Params)
	case "shared.get":
		var params struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, err
		}
		value, ok := w.manager.shared.Get(params.Key)
		return map[string]any{"found": ok, "value": json.RawMessage(value)}, nil
	case "shared.set":
		var params struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, err
		}
		if err := w.manager.shared.Set(params.Key, params.Value); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "shared.delete":
		var params struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": w.manager.shared.Delete(params.Key)}, nil
	case "shared.list":
		var params struct {
			Prefix string `json:"prefix"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, err
		}
		return map[string]any{"keys": w.manager.shared.List(params.Prefix)}, nil
	case "shared.compare_and_swap":
		var params struct {
			Key      string          `json:"key"`
			Expected json.RawMessage `json:"expected"`
			Value    json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, err
		}
		swapped, err := w.manager.shared.CompareAndSwap(params.Key, params.Expected, params.Value)
		if err != nil {
			return nil, err
		}
		return map[string]any{"swapped": swapped}, nil
	default:
		return nil, fmt.Errorf("unsupported hook request method %q", value.Method)
	}
}

func (w *worker) callTool(raw json.RawMessage) (any, error) {
	var params struct {
		Name        string          `json:"name"`
		Arguments   json.RawMessage `json:"arguments"`
		ToolContext string          `json:"tool_context"`
		Origin      string          `json:"origin"`
		Background  bool            `json:"background"`
		Target      delivery.Target `json:"target"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return nil, fmt.Errorf("tool.call requires name")
	}
	allowed := contains(w.config.Tools.Allow, name)
	if params.Background {
		allowed = contains(w.config.Tools.BackgroundAllow, name)
	}
	if !allowed {
		return nil, fmt.Errorf("tool %q is not allowed for hook %q", name, w.config.ID)
	}
	if w.manager.opts.Registry == nil {
		return nil, fmt.Errorf("tool registry is not configured")
	}
	t, ok := w.manager.opts.Registry.Get(name)
	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}
	if t.Info().ForegroundOnly && params.Background {
		return nil, fmt.Errorf("tool %q is foreground-only", name)
	}
	callCtx := context.Background()
	cancel := func() {}
	defer cancel()
	actor := security.Actor{ID: "hook:" + w.config.ID, Role: security.RoleSuperadmin}
	if !params.Background || strings.TrimSpace(params.Origin) != "" {
		token := params.ToolContext
		if params.Background {
			token = params.Origin
		}
		contextValue, ok := w.manager.takeToolContext(token, w.config.ID)
		if !ok {
			return nil, fmt.Errorf("invalid, expired or exhausted hook context")
		}
		callCtx = contextValue.Context
		actor = security.Actor{ID: contextValue.Event.Actor.ID, Platform: contextValue.Event.Platform.Name, PlatformUserID: contextValue.Event.Actor.UserID, Role: security.Role(contextValue.Event.Actor.Role), GroupRole: security.GroupRole(contextValue.Event.Actor.GroupRole), DisplayName: contextValue.Event.Actor.DisplayName}
	} else if params.Target.Empty() {
		return nil, fmt.Errorf("background tool output requires an explicit target")
	} else {
		callCtx, cancel = context.WithTimeout(callCtx, time.Duration(w.config.EventTimeoutSeconds)*time.Second)
	}
	callCtx = security.WithActor(callCtx, actor)
	started := time.Now()
	result, err := t.Call(callCtx, tool.CallRequest{ID: randomID("plugin"), Name: name, Arguments: params.Arguments})
	if w.manager.opts.Audit != nil {
		status := "ok"
		if err != nil {
			status = "error"
		}
		invocation := params.ToolContext
		if params.Background {
			invocation = params.Origin
		}
		w.manager.opts.Audit("hook.tool_call", "hook", w.config.ID, "invocation", invocation, "tool", name, "status", status, "elapsed_ms", time.Since(started).Milliseconds(), "platform", actor.Platform, "user_id", actor.PlatformUserID)
	}
	if err != nil {
		return nil, err
	}
	if result == nil {
		return map[string]any{"content": "", "segments": []llm.MessageSegment{}, "warnings": []string{}, "receipts": []delivery.Receipt{}}, nil
	}
	receipts := []delivery.Receipt{}
	for _, output := range result.Outputs {
		if !params.Target.Empty() {
			output.Target = params.Target
		}
		if w.manager.opts.Send == nil {
			return nil, fmt.Errorf("hook output sender is not configured")
		}
		receipt, err := w.manager.opts.Send(callCtx, output.Target, output)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	return map[string]any{"content": result.Content, "segments": result.Segments, "warnings": result.Warnings, "receipts": receipts}, nil
}

func (w *worker) schemas() []llm.ToolSchema {
	if w.manager.opts.Registry == nil {
		return nil
	}
	out := make([]llm.ToolSchema, 0, len(w.config.Tools.Allow))
	for _, name := range w.config.Tools.Allow {
		if t, ok := w.manager.opts.Registry.Get(name); ok {
			out = append(out, t.Schema())
		}
	}
	return out
}

func (w *worker) outputs(specs []outputSpec) ([]delivery.Output, error) {
	outputs := make([]delivery.Output, 0, len(specs))
	for _, spec := range specs {
		kind := delivery.Kind(strings.TrimSpace(spec.Kind))
		switch kind {
		case delivery.KindText, delivery.KindEmoticon, delivery.KindImage, delivery.KindFile, delivery.KindAt, delivery.KindReply:
		default:
			return nil, fmt.Errorf("unsupported hook output kind %q", spec.Kind)
		}
		path := strings.TrimSpace(spec.Path)
		if path != "" && !filepath.IsAbs(path) {
			path = filepath.Join(w.config.Dir, path)
		}
		outputs = append(outputs, delivery.Output{Kind: kind, Text: spec.Text, Name: spec.Name, AltText: spec.AltText, ReplyToPlatformMessageID: spec.ReplyToMessageID, Source: delivery.Source{URL: spec.URL, Path: path, MIMEType: spec.MIMEType}, Target: spec.Target})
	}
	return outputs, nil
}

type systemInit struct {
	Version string           `json:"version"`
	Hook    systemHook       `json:"hook"`
	Tools   []llm.ToolSchema `json:"tools"`
}

type systemHook struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Dir         string `json:"plugin_dir"`
	Cwd         string `json:"cwd"`
	SharedDir   string `json:"shared_dir"`
}

type eventHandle struct {
	Event        hook.Event        `json:"event"`
	Match        hook.MatchContext `json:"match,omitempty"`
	Continuation bool              `json:"continuation,omitempty"`
	ToolContext  string            `json:"tool_context"`
}

func eventMatch(event hook.Event) hook.MatchContext {
	if event.Metadata == nil {
		return hook.MatchContext{}
	}
	match, _ := event.Metadata["match"].(hook.MatchContext)
	return match
}

type eventResult struct {
	Status          string       `json:"status"`
	ConversationID  string       `json:"conversation_id,omitempty"`
	ExpiresAt       time.Time    `json:"expires_at,omitempty"`
	Outputs         []outputSpec `json:"outputs,omitempty"`
	Consume         bool         `json:"consume,omitempty"`
	StopPropagation bool         `json:"stop_propagation,omitempty"`
}

type outputSpec struct {
	Kind             string          `json:"kind"`
	Text             string          `json:"text,omitempty"`
	Name             string          `json:"name,omitempty"`
	AltText          string          `json:"alt_text,omitempty"`
	URL              string          `json:"url,omitempty"`
	Path             string          `json:"path,omitempty"`
	MIMEType         string          `json:"mime_type,omitempty"`
	ReplyToMessageID string          `json:"reply_to_message_id,omitempty"`
	Target           delivery.Target `json:"target,omitempty"`
}

type stderrLogger struct {
	logger *slog.Logger
	hookID string
}

func (l stderrLogger) Write(data []byte) (int, error) {
	if l.logger != nil {
		line := strings.TrimSpace(string(data))
		if line != "" {
			l.logger.Info("stateful hook stderr", "hook", l.hookID, "line", line)
		}
	}
	return len(data), nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func splitCommand(command string) ([]string, error) {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return nil, fmt.Errorf("runtime command is required")
	}
	return fields, nil
}

func resolveCwd(dir, cwd string) (string, error) {
	if filepath.IsAbs(cwd) {
		return "", fmt.Errorf("runtime cwd must be relative to plugin directory")
	}
	path := filepath.Clean(filepath.Join(dir, cwd))
	rel, err := filepath.Rel(filepath.Clean(dir), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("runtime cwd escapes plugin directory")
	}
	return path, nil
}
