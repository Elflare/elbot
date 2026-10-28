// 管理插件 worker、配置应用、替换和运行时资源关闭。

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	globalevents "elbot/internal/events"
	"elbot/internal/hook/share"
)

type Manager struct {
	mu            sync.RWMutex
	opts          Options
	workers       map[string]*worker
	transient     map[routeKey]*worker
	configs       map[string]share.RuntimeConfig
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
	// workWG joins owned asynchronous work, including tasks whose worker left an index.
	// New work is admitted under mu; counted tasks register cleanup before returning.
	workWG         sync.WaitGroup
	closeDone      chan struct{}
	closeErr       error
	workerCloseErr error         // worker 退出结果独立于可重试的媒体清理结果。
	closeMedia     chan struct{} // 串行关闭重试；等待可由调用方取消。
}

var ErrClosed = errors.New("hook runtime manager is closed")

// beginMediaCall 与关闭原子登记完整调用；取消链和在途计数归 runtime。
func (m *Manager) beginMediaCall(ctx context.Context) (context.Context, func(), error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return nil, nil, err
	}
	m.workWG.Add(1)
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.rootCtx, cancel)
	m.mu.Unlock()
	return callCtx, func() { stop(); cancel(); m.workWG.Done() }, nil
}

func NewManager(opts Options) (*Manager, error) {
	if err := os.MkdirAll(opts.SharedDir, 0o755); err != nil {
		return nil, fmt.Errorf("create shared hook directory: %w", err)
	}
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	rootCtx, rootCancel := context.WithCancel(context.Background())
	m := &Manager{
		opts:         opts,
		workers:      map[string]*worker{},
		transient:    map[routeKey]*worker{},
		configs:      map[string]share.RuntimeConfig{},
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
		closeMedia:   make(chan struct{}, 1),
	}
	go m.cleanSharedState(cleanupCtx)
	return m, nil
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
				m.opts.Log(ctx, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: level, EventName: "prune_hook_media", Module: "hook", Type: globalevents.ContentText, ContentLimit: 4096, Content: logResult.Content, ResultStatus: logResult.ResultStatus})
			}
		}
	}
}

func (m *Manager) SharedRequest(method string, params json.RawMessage) (any, error) {
	return m.shared.HandleRequest(method, params)
}

func (m *Manager) SetPluginReloadPreparer(fn func(string) (func() (globalevents.LogResult, error), error)) {
	m.mu.Lock()
	m.prepareReload = fn
	m.mu.Unlock()
}

func (m *Manager) preparePluginReload(id string) (func() (globalevents.LogResult, error), error) {
	m.mu.RLock()
	prepare := m.prepareReload
	m.mu.RUnlock()
	return prepare(id)
}

// Apply reconciles configured workers. Persistent workers start immediately;
// transient worker definitions are retained until a trigger rule matches.
func (m *Manager) Apply(configs []share.RuntimeConfig, commit func(string)) error {
	next := make(map[string]*share.RuntimeConfig, len(configs))
	for i := range configs {
		next[configs[i].ID] = &configs[i]
	}
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return ErrClosed
	}
	for id := range m.configs {
		if _, ok := next[id]; !ok {
			next[id] = nil
		}
	}
	m.mu.RUnlock()
	ids := make([]string, 0, len(next))
	for id := range next {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	failures := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, failures[i] = m.ReplacePlugin(id, next[id], func() { commit(id) })
		}()
	}
	wg.Wait()
	return errors.Join(failures...)
}

func (m *Manager) validateConfig(config share.RuntimeConfig) error {
	for _, name := range config.Tools.Allow {
		if _, ok := m.opts.Tools.ToolInfo(name); !ok {
			return fmt.Errorf("runtime tools.allow references unknown tool %q", name)
		}
	}
	return nil
}

func (m *Manager) ValidatePlugin(config share.RuntimeConfig) error {
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

// ReplacePlugin stops this plugin's old workers before publishing its candidate.
// A nil config removes the worker definition. commit publishes its ordinary rules.
func (m *Manager) ReplacePlugin(id string, config *share.RuntimeConfig, commit func()) (globalevents.LogResult, error) {
	if config != nil {
		if err := m.validateConfig(*config); err != nil {
			_ = m.opts.Notify(context.WithoutCancel(m.rootCtx), globalevents.Notification{Text: fmt.Sprintf("Hook %s 配置应用失败：%v", id, err), Level: slog.LevelWarn})
			return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, fmt.Errorf("hook %s config: %w", id, err)
		}
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return globalevents.LogResult{ResultStatus: globalevents.ResultCanceled, Content: ErrClosed.Error()}, ErrClosed
	}
	workers := make([]*worker, 0)
	if old := m.workers[id]; old != nil {
		workers = append(workers, old)
	}
	for _, old := range m.transient {
		if old.config.ID == id {
			workers = append(workers, old)
		}
	}
	// Keep old workers indexed while stopping; their own stop state blocks new dispatch.
	for _, old := range workers {
		old.mu.Lock()
		old.manualStop = true
		old.mu.Unlock()
	}
	if len(workers) > 0 {
		m.mu.Unlock()
		failures := make([]error, len(workers))
		var wg sync.WaitGroup
		for i, old := range workers {
			wg.Add(1)
			go func() { defer wg.Done(); _, failures[i] = old.stop(context.Background()) }()
		}
		wg.Wait()
		if err := errors.Join(failures...); err != nil {
			return globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: fmt.Sprintf("hook %s stop: %v", id, err)}, fmt.Errorf("hook %s stop: %w", id, err)
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return globalevents.LogResult{ResultStatus: globalevents.ResultCanceled, Content: ErrClosed.Error()}, ErrClosed
		}
	}
	for key, old := range m.transient {
		if old.config.ID == id {
			delete(m.transient, key)
		}
	}
	m.clearRoutesLocked(id)
	for token, value := range m.tokens {
		if value.HookID == id {
			delete(m.tokens, token)
		}
	}
	delete(m.workers, id)
	if config == nil {
		delete(m.configs, id)
	} else {
		m.configs[id] = *config
		if config.ModeOrOnce() == share.RuntimeModePersistent {
			m.workers[id] = newWorker(m, *config)
		}
	}
	next := m.workers[id]
	commit()
	m.mu.Unlock()
	if next != nil {
		if err := m.startWorker(next); err != nil {
			return globalevents.LogResult{ResultStatus: globalevents.ResultCanceled, Content: err.Error()}, err
		}
	}
	return globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded, Content: "Hook definition published; persistent startup scheduled"}, nil
}

// Done closes after workers and shared resources actually finish shutting down.
func (m *Manager) Done() <-chan struct{} { return m.closeDone }

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		if err := m.waitClose(ctx, done); err == nil || ctx.Err() != nil {
			return err
		}
		select {
		case m.closeMedia <- struct{}{}:
			defer func() { <-m.closeMedia }()
		case <-ctx.Done():
			return ctx.Err()
		}
		_, err := m.media.prune(ctx, time.Now(), true)
		m.mu.Lock()
		m.closeErr = errors.Join(m.workerCloseErr, err)
		err = m.closeErr
		m.mu.Unlock()
		return err
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

	m.sharedCancel()
	go m.finishClose(ctx, workers)
	return m.waitClose(ctx, m.closeDone)
}

func (m *Manager) waitClose(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		m.mu.RLock()
		err := m.closeErr
		m.mu.RUnlock()
		return err
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
	m.workWG.Wait()
	<-m.sharedDone

	logResult, err := m.media.prune(ctx, time.Now(), true)
	if logResult.ResultStatus != globalevents.ResultSkipped {
		level := slog.LevelInfo
		if logResult.ResultStatus == globalevents.ResultFailed {
			level = slog.LevelError
		}
		m.opts.Log(ctx, globalevents.LogRecord{Category: globalevents.LogRuntime, Level: level, EventName: "prune_hook_media", Module: "hook", Type: globalevents.ContentText, ContentLimit: 4096, Content: logResult.Content, ResultStatus: logResult.ResultStatus})
	}
	m.mu.Lock()
	m.workerCloseErr = errors.Join(workerErrs...)
	m.closeErr = errors.Join(m.workerCloseErr, err)
	close(m.closeDone)
	m.mu.Unlock()
}

func (m *Manager) startWorker(worker *worker) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	m.workWG.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.workWG.Done()
		worker.run()
	}()
	return nil
}

func (m *Manager) isClosed() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.closed
}

func (m *Manager) List() []share.RuntimeInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	itemsByID := map[string]Info{}
	for id, config := range m.configs {
		itemsByID[id] = Info{ID: id, Description: config.Description, Mode: config.ModeOrOnce()}
	}
	for id, worker := range m.workers {
		itemsByID[id] = worker.info()
	}
	for _, worker := range m.transient {
		info := itemsByID[worker.config.ID]

		workerInfo := worker.info()
		info.Active += workerInfo.Active
		if workerInfo.Status == StatusRunning {
			info.Status = StatusRunning
		} else if info.Status == "" {
			info.Status = workerInfo.Status
		}
		itemsByID[worker.config.ID] = info
	}
	for _, lease := range m.routes {
		info := itemsByID[lease.HookID]
		info.Waiting++
		itemsByID[lease.HookID] = info
	}
	items := make([]share.RuntimeInfo, 0, len(itemsByID))
	for _, info := range itemsByID {
		if info.Mode == share.RuntimeModeTransient && info.Status == "" {
			info.Status = StatusStopped
		}
		items = append(items, share.RuntimeInfo{ID: info.ID, Description: info.Description, Mode: string(info.Mode), Status: string(info.Status), Detail: info.Detail, Active: info.Active, Waiting: info.Waiting})
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
	if !ok || config.ModeOrOnce() != share.RuntimeModeTransient {
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.workers[strings.TrimSpace(id)]
}

func (m *Manager) Handle(ctx context.Context, id string, event share.Event, defaults share.Control) (share.Event, globalevents.LogResult, error) {
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
	case share.RuntimeModePersistent:
		worker = m.worker(id)
	case share.RuntimeModeTransient:
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
	if config.ModeOrOnce() == share.RuntimeModeTransient && (err != nil || !m.hasLease(event)) {
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

func (m *Manager) startTransient(ctx context.Context, config share.RuntimeConfig, key routeKey) (*worker, globalevents.LogResult, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: ErrClosed.Error()}, ErrClosed
	}
	current, ok := m.configs[config.ID]
	if !ok {
		m.mu.Unlock()
		err := fmt.Errorf("hook worker %q is no longer configured", config.ID)
		return nil, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: err.Error()}, err
	}
	config = current
	for _, old := range m.transient {
		if old.config.ID == config.ID {
			old.mu.Lock()
			stopping := old.manualStop || old.stopping
			old.mu.Unlock()
			if stopping {
				m.mu.Unlock()
				err := fmt.Errorf("hook worker %q is stopping", config.ID)
				return nil, globalevents.LogResult{ResultStatus: globalevents.ResultRejected, Content: err.Error()}, err
			}
		}
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
	if m.closed {
		m.mu.Unlock()
		return // Close already owns stopping the indexed workers.
	}
	worker := m.transient[key]
	if worker == nil || (wanted != nil && worker != wanted) {
		m.mu.Unlock()
		return
	}
	delete(m.transient, key)
	delete(m.routes, key)
	m.workWG.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.workWG.Done()
		_, _ = worker.stop(context.Background())
	}()
}
