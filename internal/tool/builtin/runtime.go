package builtin

import (
	"path/filepath"

	"elbot/internal/fileops"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/processenv"
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
	FileRollback        *fileops.Service
}

type RuntimeOptions struct {
	FileRollback           *fileops.Service
	ConfigDir              string
	RuntimeInfo            runtimeinfo.Info
	ChatHistory            storage.ChatHistoryRepository
	Media                  *media.Manager
	ResidentMemoryMaxUnits resident.Limits
	ProcessEnv             processenv.Environment
}

func NewRuntime(opts RuntimeOptions) (*Runtime, error) {
	info := opts.RuntimeInfo
	info.ConfigDir = opts.ConfigDir
	info = info.Normalize()
	registry := tool.NewRegistry()
	residentStore := resident.NewStoreWithLimits(filepath.Join(opts.ConfigDir, "memories.toml"), opts.ResidentMemoryMaxUnits)
	skillManager := skill.NewManager(filepath.Join(opts.ConfigDir, "skills"), registry, opts.ProcessEnv)
	fileManager := NewFileManager(info.SandboxRoot, info.FileDelivery)
	fileManager.Media = opts.Media
	runtime := &Runtime{Registry: registry, ResidentMemoryStore: residentStore, SkillManager: skillManager, FileManager: fileManager, FileRollback: opts.FileRollback}
	if err := RegisterAll(registry, RegisterOptions{
		RuntimeInfo:         info,
		FileRollback:        runtime.FileRollback,
		ResidentMemoryStore: residentStore,
		SkillManager:        skillManager,
		ChatHistory:         opts.ChatHistory,
		LongMemoryDir:       filepath.Join(opts.ConfigDir, "long_memory"),
		FileManager:         fileManager,
		ProcessEnv:          opts.ProcessEnv,
	}); err != nil {
		return nil, err
	}
	return runtime, nil
}
