package builtin

import (
	"fmt"
	"path/filepath"

	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/memory/resident"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/tool/runtimeinfo"
	"elbot/internal/tool/skill"
)

type Runtime struct {
	Registry            *tool.Registry
	ResidentMemoryStore *resident.Store
	SkillManager        *skill.Manager
	FileManager         *FileManager
}

type RuntimeOptions struct {
	ConfigDir              string
	RuntimeInfo            runtimeinfo.Info
	CronService            *elcron.Service
	ChatHistory            storage.ChatHistoryRepository
	SandboxRoot            string
	FileDelivery           config.FileDeliveryConfig
	ResidentMemoryMaxUnits resident.Limits
}

func NewRuntime(opts RuntimeOptions) (*Runtime, error) {
	if opts.ConfigDir == "" {
		return nil, fmt.Errorf("builtin runtime config dir is required")
	}
	info := opts.RuntimeInfo
	if info.ConfigDir == "" {
		info.ConfigDir = opts.ConfigDir
	}
	if info.SandboxRoot == "" {
		info.SandboxRoot = opts.SandboxRoot
	}
	if info.FileDelivery == (config.FileDeliveryConfig{}) {
		info.FileDelivery = opts.FileDelivery
	}
	info = info.Normalize()
	registry := tool.NewRegistry()
	residentStore := resident.NewStoreWithLimits(filepath.Join(opts.ConfigDir, "memories.toml"), opts.ResidentMemoryMaxUnits)
	skillManager := skill.NewManager(filepath.Join(opts.ConfigDir, "skills"), registry)
	fileManager := NewFileManager(info.SandboxRoot, info.FileDelivery)
	runtime := &Runtime{Registry: registry, ResidentMemoryStore: residentStore, SkillManager: skillManager, FileManager: fileManager}
	if err := RegisterAll(registry, RegisterOptions{
		RuntimeInfo:         info,
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
