package builtin

import (
	"fmt"
	"path/filepath"

	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/memory/resident"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/tool/skill"
)

type Runtime struct {
	Registry            *tool.Registry
	ResidentMemoryStore *resident.Store
	SkillManager        *skill.Manager
	FileManager         *FileManager
}

type RuntimeOptions struct {
	ConfigDir    string
	CronService  *elcron.Service
	ChatHistory  storage.ChatHistoryRepository
	SandboxRoot  string
	FileDelivery config.FileDeliveryConfig
}

func NewRuntime(opts RuntimeOptions) (*Runtime, error) {
	if opts.ConfigDir == "" {
		return nil, fmt.Errorf("builtin runtime config dir is required")
	}
	registry := tool.NewRegistry()
	residentStore := resident.NewStore(filepath.Join(opts.ConfigDir, "memories.toml"))
	skillManager := skill.NewManager(filepath.Join(opts.ConfigDir, "skills"), registry)
	fileManager := NewFileManager(opts.SandboxRoot, opts.FileDelivery)
	runtime := &Runtime{Registry: registry, ResidentMemoryStore: residentStore, SkillManager: skillManager, FileManager: fileManager}
	if err := RegisterAll(registry, RegisterOptions{
		ResidentMemoryStore: residentStore,
		SkillManager:        skillManager,
		CronService:         opts.CronService,
		ChatHistory:         opts.ChatHistory,
		LongMemoryDir:       filepath.Join(opts.ConfigDir, "long_memory"),
		FileManager:         fileManager,
	}); err != nil {
		return nil, err
	}
	return runtime, nil
}
