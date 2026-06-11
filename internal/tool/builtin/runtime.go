package builtin

import (
	"fmt"
	"path/filepath"

	elcron "elbot/internal/cron"
	"elbot/internal/memory/resident"
	"elbot/internal/tool"
	"elbot/internal/tool/skill"
)

type Runtime struct {
	Registry            *tool.Registry
	ResidentMemoryStore *resident.Store
	SkillManager        *skill.Manager
}

type RuntimeOptions struct {
	ConfigDir   string
	CronService *elcron.Service
}

func NewRuntime(opts RuntimeOptions) (*Runtime, error) {
	if opts.ConfigDir == "" {
		return nil, fmt.Errorf("builtin runtime config dir is required")
	}
	registry := tool.NewRegistry()
	residentStore := resident.NewStore(filepath.Join(opts.ConfigDir, "memories.toml"))
	skillManager := skill.NewManager("", registry)
	runtime := &Runtime{Registry: registry, ResidentMemoryStore: residentStore, SkillManager: skillManager}
	if err := RegisterAll(registry, RegisterOptions{
		ResidentMemoryStore: residentStore,
		SkillManager:        skillManager,
		CronService:         opts.CronService,
		LongMemoryDir:       filepath.Join(opts.ConfigDir, "long_memory"),
	}); err != nil {
		return nil, err
	}
	return runtime, nil
}
