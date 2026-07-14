package runtime

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

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
	sharedCancel  context.CancelFunc
	sharedDone    chan struct{}
	prepareReload func(string) (func() error, error)
}

func NewManager(opts Options) *Manager {
	if strings.TrimSpace(opts.SharedDir) != "" {
		_ = os.MkdirAll(opts.SharedDir, 0o755)
	}
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	m := &Manager{
		opts:         opts,
		workers:      map[string]*worker{},
		transient:    map[routeKey]*worker{},
		configs:      map[string]Config{},
		routes:       map[routeKey]lease{},
		running:      map[routeKey]invocation{},
		tokens:       map[string]toolContext{},
		shared:       NewSharedState(),
		sharedCancel: cleanupCancel,
		sharedDone:   make(chan struct{}),
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
		}
	}
}

func (m *Manager) SharedState() *SharedState {
	if m == nil {
		return nil
	}
	return m.shared
}

func (m *Manager) SetPluginReloadPreparer(fn func(string) (func() error, error)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.prepareReload = fn
	m.mu.Unlock()
}

func (m *Manager) preparePluginReload(id string) (func() error, error) {
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
		old.stop(context.Background())
	}
	for _, old := range previousTransient {
		old.stop(context.Background())
	}
	for _, worker := range workers {
		go worker.run()
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
func (m *Manager) ReplacePlugin(config Config) error {
	if err := m.ValidatePlugin(config); err != nil {
		return err
	}
	id := strings.TrimSpace(config.ID)
	m.mu.Lock()
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
		old.stop(context.Background())
	}
	if next != nil {
		go next.run()
	}
	return nil
}

func (m *Manager) Close(ctx context.Context) {
	if m == nil {
		return
	}
	if m.sharedCancel != nil {
		m.sharedCancel()
		select {
		case <-m.sharedDone:
		case <-ctx.Done():
		}
	}
	m.mu.RLock()
	workers := make([]*worker, 0, len(m.workers)+len(m.transient))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	for _, worker := range m.transient {
		workers = append(workers, worker)
	}
	m.mu.RUnlock()
	for _, worker := range workers {
		worker.stop(ctx)
	}
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
	worker := m.worker(id)
	if worker == nil {
		return fmt.Errorf("persistent hook %q not found", id)
	}
	worker.start()
	return worker.waitReady(context.Background())
}

func (m *Manager) Stop(ctx context.Context, id string) (bool, error) {
	id = strings.TrimSpace(id)
	persistent := m.worker(id)
	if persistent != nil {
		persistent.stop(ctx)
		m.mu.Lock()
		m.clearRoutesLocked(id)
		m.mu.Unlock()
		return true, nil
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
	for _, transient := range workers {
		transient.stop(ctx)
	}
	return true, nil
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

func (m *Manager) Handle(ctx context.Context, id string, event hook.Event, defaults hook.Control) (hook.Event, error) {
	if m.hasLease(event) {
		return event, nil
	}
	if skipped, _ := event.Metadata[skipHookIDMetadataKey].(string); skipped == id {
		return event, nil
	}
	m.mu.RLock()
	config, configured := m.configs[id]
	m.mu.RUnlock()
	if !configured {
		return event, fmt.Errorf("hook worker %q is not configured", id)
	}
	var worker *worker
	switch config.ModeOrOnce() {
	case ModePersistent:
		worker = m.worker(id)
	case ModeTransient:
		var err error
		worker, err = m.startTransient(ctx, config, routeKeyFor(event))
		if err != nil {
			return event, err
		}
	default:
		return event, fmt.Errorf("hook %q is not configured as a worker", id)
	}
	if worker == nil {
		return event, fmt.Errorf("hook worker %q is not available", id)
	}
	if worker.config.Block.Blocks(event) {
		return event, nil
	}
	updated, err := worker.handle(ctx, event, false, defaults)
	if config.ModeOrOnce() == ModeTransient && (err != nil || !m.hasLease(event)) {
		m.stopTransient(routeKeyFor(event), worker)
	}
	if err == nil {
		if updated.Metadata == nil {
			updated.Metadata = map[string]any{}
		}
		updated.Metadata[dispatchedMetadataKey] = true
	}
	return updated, err
}

func (m *Manager) startTransient(ctx context.Context, config Config, key routeKey) (*worker, error) {
	m.mu.Lock()
	worker := m.transient[key]
	if worker == nil {
		worker = newWorker(m, config)
		m.transient[key] = worker
		go worker.run()
	}
	m.mu.Unlock()
	if err := worker.waitReady(ctx); err != nil {
		m.stopTransient(key, worker)
		return nil, err
	}
	return worker, nil
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
	go worker.stop(context.Background())
}
