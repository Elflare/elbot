package runtime

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"elbot/internal/hook"
)

type Manager struct {
	mu            sync.RWMutex
	opts          Options
	workers       map[string]*worker
	routes        map[routeKey]lease
	running       map[routeKey]invocation
	tokens        map[string]toolContext
	shared        *SharedState
	prepareReload func(string) (func() error, error)
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
	nextWorkers := make(map[string]*worker, len(next))
	workers := make([]*worker, 0, len(next))
	for id, config := range next {
		worker := newWorker(m, config)
		nextWorkers[id] = worker
		workers = append(workers, worker)
	}

	m.mu.Lock()
	previous := m.workers
	m.workers = nextWorkers
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
		return fmt.Errorf("stateful hook runtime is not configured")
	}
	if err := m.validateConfig(config); err != nil {
		return err
	}
	current := m.worker(config.ID)
	if current == nil {
		return fmt.Errorf("stateful hook %q is not configured", config.ID)
	}
	if current.config.Dir != config.Dir || current.config.ConfigPath != config.ConfigPath {
		return fmt.Errorf("stateful hook %q cannot change plugin path during self reload", config.ID)
	}
	return nil
}

// ReplacePlugin swaps and restarts one persistent worker without disturbing
// other plugins or their continuation routes.
func (m *Manager) ReplacePlugin(config Config) error {
	if err := m.ValidatePlugin(config); err != nil {
		return err
	}
	id := strings.TrimSpace(config.ID)
	next := newWorker(m, config)
	m.mu.Lock()
	old := m.workers[id]
	if old == nil {
		m.mu.Unlock()
		return fmt.Errorf("stateful hook %q is not configured", id)
	}
	m.workers[id] = next
	m.clearRoutesLocked(id)
	for token, value := range m.tokens {
		if value.HookID == id {
			delete(m.tokens, token)
		}
	}
	m.mu.Unlock()

	old.stop(context.Background())
	go next.run()
	return nil
}

func (m *Manager) Close(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.RLock()
	workers := make([]*worker, 0, len(m.workers))
	for _, worker := range m.workers {
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
	items := make([]Info, 0, len(m.workers))
	for _, worker := range m.workers {
		items = append(items, worker.info())
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (m *Manager) Start(id string) error {
	worker := m.worker(id)
	if worker == nil {
		return fmt.Errorf("stateful hook %q not found", id)
	}
	worker.start()
	return nil
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	worker := m.worker(id)
	if worker == nil {
		return fmt.Errorf("stateful hook %q not found", id)
	}
	worker.stop(ctx)
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
	worker := m.worker(id)
	if worker == nil {
		return event, fmt.Errorf("stateful hook %q is not configured", id)
	}
	if worker.config.Block.Blocks(event) {
		return event, nil
	}
	updated, err := worker.handle(ctx, event, false)
	if err == nil {
		if updated.Metadata == nil {
			updated.Metadata = map[string]any{}
		}
		updated.Metadata[dispatchedMetadataKey] = true
	}
	return updated, err
}
