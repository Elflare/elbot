package module

import (
	"elbot/internal/config"
	"elbot/internal/media"
	"elbot/internal/memory/resident"
	"elbot/internal/processenv"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/runtimeinfo"
)

// ModuleDeps declares Tool's configuration and lower-level capabilities only.
type ModuleDeps struct {
	ConfigDir            string
	RuntimeInfo          runtimeinfo.Info
	Store                storage.Store
	ChatHistory          storage.ChatHistoryRepository
	Media                *media.Manager
	Permission           *security.Permission
	ProcessEnv           processenv.Environment
	ResidentMemoryLimits resident.Limits
	TagsPath             string
	Tags                 config.ToolTagsConfig
}
