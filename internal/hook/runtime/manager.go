package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	globalevents "elbot/internal/events"
	"elbot/internal/hook"
)

type Manager struct {
	mu            sync.RWMutex
	opts          Options
	workers       map[string]*worker
	transient     map[routeKey]*worker
	configs       map[string]Config
	routes        map[routeKey]lease
	running       map[routeKey]invocation
	tokens        map[string]toolContext
	shared        *SharedState
	media         *mediaBridge
	sharedCancel  context.CancelFunc
	sharedDone    chan struct{}
	prepareReload func(string) (func() (globalevents.LogResult, error), error)
	rootCtx       context.Context
	rootCancel    context.CancelFunc
	closed        bool
	startWG       sync.WaitGroup
	closeDone     chan struct{}
	closeErr      error
}

var ErrClosed = errors.New("hook runtime manager is closed")

func NewManager(opts Options) *Manager {
	if strings.TrimSpace(opts.SharedDir) != "" {
		_ = os.MkdirAll(opts.SharedDir, 0o755)
	}
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	rootCtx, rootCancel := context.WithCancel(context.Background())
	m := &Manager{
		opts:         opts,
		workers:      map[string]*worker{},
		transient:    map[routeKey]*worker{},
		configs:      map[string]Config{},
		routes:       map[routeKey]lease{},
		running:      map[routeKey]invocation{},
		tokens:       map[string]toolContext{},
		shared:       NewSharedState(),
		media:        newMediaBridge(opts.Media),
		sharedCancel: cleanupCancel,
		sharedDone:   make(chan struct{}),
		rootCtx:      rootCtx,
		rootCancel:   rootCancel,
		closeDone:    make(chan struct{}),
	}
	go m.cleanSharedState(cleanupCtx)
	return m
}

func (m *Manager) cleanSharedState(ctx context.Context) {
	defer close(m.sharedDone)
	ticker := time.NewTicker(sharedCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.shared.PruneExpired()
			if logResult, _ := m.media.prune(ctx, time.Now(), false); logResult.ResultStatus != globalevents.ResultSkipped {
				level := slog.LevelInfo
				if logResult.ResultStatus == globalevents.ResultFailed {
					level = slog.LevelError
				}
				hook.LogHook(ctx, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: level, EventName: "prune_hook_media", Module: "hook", Type: globalevents.ContentText, ContentLimit: 4096, Content: logResult.Content, ResultStatus: logResult.ResultStatus})
			}
		}
	}
}

func (m *Manager) SharedState() *SharedState {
	if m == nil {
		return nil
	}
	return m.shared
}

func (m *Manager) SetPluginReloadPreparer(fn func(string) (func() (globalevents.LogResult, error), error)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.prepareReload = fn
	m.mu.Unlock()
}

func (m *Manager) preparePluginReload(id string) (func() (globalevents.LogResult, error), error) {
	m.mu.RLock()
	prepare := m.prepareReload
	m.mu.RUnlock()
	if prepare == nil {
		return nil, fmt.Errorf("hook plugin reload is not configured")
	}
	return prepare(id)
}

// Apply reconciles configured workers. Persistent workers start immediately;
// transient worker definitions are retained until a trigger rule matches.
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
			return fmt.Errorf("duplicate hook worker %q", config.ID)
		}
		next[config.ID] = config
	}
	nextWorkers := make(map[string]*worker, len(next))
	workers := make([]*worker, 0, len(next))
	for id, config := range next {
		if config.ModeOrOnce() != ModePersistent {
			continue
		}
		worker := newWorker(m, config)
		nextWorkers[id] = worker
		workers = append(workers, worker)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	previous := m.workers
	previousTransient := m.transient
	m.workers = nextWorkers
	m.transient = map[routeKey]*worker{}
	m.configs = next
	previousIDs := make(map[string]bool, len(previous))
	for id := range previous {
		previousIDs[id] = true
		m.clearRoutesLocked(id)
	}
	for token, value := range m.tokens {
		if previousIDs[value.HookID] {
			delete(m.tokens, token)
		}
	}
	m.mu.Unlock()

	for _, old := range previous {
		_, _ = old.stop(context.Background())
	}
	for _, old := range previousTransient {
		_, _ = old.stop(context.Background())
	}
	for _, worker := range workers {
		if err := m.startWorker(worker); err != nil {
			return err
		}
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

func (m *Manager) ValidatePlugin(config Config) error {
	if m == nil {
		return fmt.Errorf("hook runtime is not configured")
	}
	if !config.IsWorker() {
		return fmt.Errorf("hook %q is not a worker", config.ID)
	}
	if err := m.validateConfig(config); err != nil {
		return err
	}
	m.mu.RLock()
	current, ok := m.configs[config.ID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("hook worker %q is not configured", config.ID)
	}
	if current.Dir != config.Dir || current.ConfigPath != config.ConfigPath || current.ModeOrOnce() != config.ModeOrOnce() {
		return fmt.Errorf("hook worker %q cannot change plugin path or mode during self reload", config.ID)
	}
	return nil
}

// ReplacePlugin replaces one worker definition and restarts it when persistent.
func (m *Manager) ReplacePlugin(config Config) (globalevents.LogResult, error) {
	if err := m.ValidatePlugin(config); err != nil {
		return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	id := strings.TrimSpace(config.ID)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: ErrClosed.Error()}, ErrClosed
	}
	old := m.workers[id]
	m.configs[id] = config
	m.clearRoutesLocked(id)
	for token, value := range m.tokens {
		if value.HookID == id {
			delete(m.tokens, token)
		}
	}
	if config.ModeOrOnce() == ModePersistent {
		m.workers[id] = newWorker(m, config)
	} else {
		delete(m.workers, id)
	}
	next := m.workers[id]
	m.mu.Unlock()

	if old != nil {
		if logResult, err := old.stop(context.Background()); err != nil {
			return logResult, err
		}
	}
	if next != nil {
		err := m.startWorker(next)
		if err != nil {
			return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
		}
	}
	return globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "Hook worker definition replaced; startup scheduled"}, nil
}

// Done closes after workers and shared resources actually finish shutting down.
func (m *Manager) Done() <-chan struct{} { return m.closeDone }

func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		return m.waitClose(ctx, done)
	}
	m.closed = true
	m.rootCancel()
	workers := make([]*worker, 0, len(m.workers)+len(m.transient))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	for _, worker := range m.transient {
		workers = append(workers, worker)
	}
	m.routes = map[routeKey]lease{}
	m.running = map[routeKey]invocation{}
	m.tokens = map[string]toolContext{}
	m.mu.Unlock()

	if m.sharedCancel != nil {
		m.sharedCancel()
	}
	go m.finishClose(ctx, workers)
	return m.waitClose(ctx, m.closeDone)
}

func (m *Manager) waitClose(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return m.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) finishClose(ctx context.Context, workers []*worker) {
	workerErrs := make([]error, len(workers))
	var wg sync.WaitGroup
	for i, worker := range workers {
		i, worker := i, worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, workerErrs[i] = worker.stop(context.Background())
		}()
	}
	wg.Wait()
	m.startWG.Wait()
	if m.sharedDone != nil {
		<-m.sharedDone
	}

	logResult, err := m.media.prune(ctx, time.Now(), true)
	if logResult.ResultStatus != globalevents.ResultSkipped {
		level := slog.LevelInfo
		if logResult.ResultStatus == globalevents.ResultFailed {
			level = slog.LevelError
		}
		hook.LogHook(ctx, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: level, EventName: "prune_hook_media", Module: "hook", Type: globalevents.ContentText, ContentLimit: 4096, Content: logResult.Content, ResultStatus: logResult.ResultStatus})
	}
	m.mu.Lock()
	m.closeErr = errors.Join(append(workerErrs, err)...)
	close(m.closeDone)
	m.mu.Unlock()
}

func (m *Manager) startWorker(worker *worker) error {
	if m == nil || worker == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	m.startWG.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.startWG.Done()
		worker.run()
	}()
	return nil
}

func (m *Manager) isClosed() bool {
	if m == nil {
		return true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.closed
}

func (m *Manager) List() []Info {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	itemsByID := map[string]Info{}
	for id, config := range m.configs {
		itemsByID[id] = Info{ID: id, Description: config.Description, Mode: config.ModeOrOnce()}
	}
	for id, worker := range m.workers {
		itemsByID[id] = worker.info()
	}
	for key, worker := range m.transient {
		info := itemsByID[worker.config.ID]
		if info.ID == "" {
			info = Info{ID: worker.config.ID, Description: worker.config.Description, Mode: ModeTransient}
		}
		workerInfo := worker.info()
		info.Active += workerInfo.Active
		if workerInfo.Status == StatusRunning {
			info.Status = StatusRunning
		} else if info.Status == "" {
			info.Status = workerInfo.Status
		}
		itemsByID[worker.config.ID] = info
		_ = key
	}
	for _, lease := range m.routes {
		info := itemsByID[lease.HookID]
		info.Waiting++
		itemsByID[lease.HookID] = info
	}
	items := make([]Info, 0, len(itemsByID))
	for _, info := range itemsByID {
		if info.Mode == ModeTransient && info.Status == "" {
			info.Status = StatusStopped
		}
		items = append(items, info)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (m *Manager) Start(id string) error {
	if m.isClosed() {
		return ErrClosed
	}
	worker := m.worker(id)
	if worker == nil {
		return fmt.Errorf("persistent hook %q not found", id)
	}
	if err := worker.start(); err != nil {
		return err
	}
	_, err := worker.waitReady(context.Background())
	return err
}

func (m *Manager) Stop(ctx context.Context, id string) (bool, error) {
	id = strings.TrimSpace(id)
	persistent := m.worker(id)
	if persistent != nil {
		_, stopErr := persistent.stop(ctx)
		m.mu.Lock()
		m.clearRoutesLocked(id)
		m.mu.Unlock()
		return true, stopErr
	}
	m.mu.Lock()
	config, ok := m.configs[id]
	if !ok || config.ModeOrOnce() != ModeTransient {
		m.mu.Unlock()
		return false, fmt.Errorf("hook worker %q not found", id)
	}
	workers := make([]*worker, 0)
	for key, transient := range m.transient {
		if transient.config.ID == id {
			delete(m.transient, key)
			delete(m.routes, key)
			workers = append(workers, transient)
		}
	}
	m.clearRoutesLocked(id)
	m.mu.Unlock()
	stopErrs := make([]error, len(workers))
	for i, transient := range workers {
		_, stopErrs[i] = transient.stop(ctx)
	}
	return true, errors.Join(stopErrs...)
}

func (m *Manager) Restart(ctx context.Context, id string) error {
	if _, err := m.Stop(ctx, id); err != nil {
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

func (m *Manager) Handle(ctx context.Context, id string, event hook.Event, defaults hook.Control) (hook.Event, globalevents.LogResult, error) {
	if m.hasLease(event) {
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "worker not dispatched"}, nil
	}
	if skipped, _ := event.Metadata[skipHookIDMetadataKey].(string); skipped == id {
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "worker not dispatched"}, nil
	}
	m.mu.RLock()
	config, configured := m.configs[id]
	m.mu.RUnlock()
	if !configured {
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("hook worker %q is not configured", id)}, fmt.Errorf("hook worker %q is not configured", id)
	}
	var worker *worker
	switch config.ModeOrOnce() {
	case ModePersistent:
		worker = m.worker(id)
	case ModeTransient:
		var err error
		var logResult globalevents.LogResult
		worker, logResult, err = m.startTransient(ctx, config, routeKeyFor(event))
		if err != nil {
			return event, logResult, err
		}
	default:
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("hook %q is not configured as a worker", id)}, fmt.Errorf("hook %q is not configured as a worker", id)
	}
	if worker == nil {
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("hook worker %q is not available", id)}, fmt.Errorf("hook worker %q is not available", id)
	}
	if worker.config.Block.Blocks(event) {
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultSkipped, Content: "worker not dispatched"}, nil
	}
	updated, logResult, err := worker.handle(ctx, event, false, defaults)
	if config.ModeOrOnce() == ModeTransient && (err != nil || !m.hasLease(event)) {
		m.stopTransient(routeKeyFor(event), worker)
	}
	if err == nil {
		if updated.Metadata == nil {
			updated.Metadata = map[string]any{}
		}
		updated.Metadata[dispatchedMetadataKey] = true
	}
	return updated, logResult, err
}

func (m *Manager) startTransient(ctx context.Context, config Config, key routeKey) (*worker, globalevents.LogResult, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: ErrClosed.Error()}, ErrClosed
	}
	worker := m.transient[key]
	created := false
	if worker == nil {
		worker = newWorker(m, config)
		m.transient[key] = worker
		created = true
	}
	m.mu.Unlock()
	if created {
		if err := m.startWorker(worker); err != nil {
			return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
		}
	}
	logResult, err := worker.waitReady(ctx)
	if err != nil {
		m.stopTransient(key, worker)
		return nil, logResult, err
	}
	return worker, logResult, nil
}

func (m *Manager) stopTransient(key routeKey, wanted *worker) {
	m.mu.Lock()
	worker := m.transient[key]
	if worker == nil || (wanted != nil && worker != wanted) {
		m.mu.Unlock()
		return
	}
	delete(m.transient, key)
	delete(m.routes, key)
	m.mu.Unlock()
	go func() { _, _ = worker.stop(context.Background()) }()
}
