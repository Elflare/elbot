package module

import (
	"context"
	"fmt"
	"sync"
	"time"

	"elbot/internal/fileops"
	"elbot/internal/memory/resident"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/skill"
	"elbot/internal/toolrun"
)

// Module owns the tool catalog and its shared execution, preload and state services.
type Module struct {
	catalog   *builtin.Runtime
	engine    *toolrun.Manager
	preloader *toolrun.PreloadService
	state     *toolrun.StateService
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	started   bool
	closed    bool
	done      chan struct{}
}

func NewModule(ctx context.Context, deps ModuleDeps) (*Module, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &Module{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	catalog, err := builtin.NewRuntime(builtin.RuntimeOptions{FileRollback: fileops.NewService(nil), ConfigDir: deps.ConfigDir, RuntimeInfo: deps.RuntimeInfo, ChatHistory: deps.ChatHistory, Media: deps.Media, ResidentMemoryMaxUnits: deps.ResidentMemoryLimits, ProcessEnv: deps.ProcessEnv})
	if err != nil {
		return m, err
	}
	m.catalog = catalog
	m.engine = toolrun.NewManager(catalog.Registry, deps.Permission)
	m.engine.Media = deps.Media
	m.preloader = toolrun.NewPreloadService(toolrun.PreloadOptions{Registry: catalog.Registry, TagsPath: deps.TagsPath, Tags: deps.Tags})
	m.state = toolrun.NewStateService(deps.Store)
	return m, nil
}
func (m *Module) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("tool module is closed")
	}
	if m.started {
		return nil
	}
	m.started = true
	done := m.catalog.SkillManager.StartDelayedReload(m.ctx, time.Second)
	go func() { <-done; close(m.done) }()
	return nil
}
func (m *Module) Registry() *tool.Registry           { return m.catalog.Registry }
func (m *Module) Manager() *toolrun.Manager          { return m.engine }
func (m *Module) Preloader() *toolrun.PreloadService { return m.preloader }
func (m *Module) State() *toolrun.StateService       { return m.state }
func (m *Module) Skills() *skill.Manager             { return m.catalog.SkillManager }
func (m *Module) Memory() *resident.Store            { return m.catalog.ResidentMemoryStore }
func (m *Module) Files() *fileops.Service            { return m.catalog.FileRollback }
