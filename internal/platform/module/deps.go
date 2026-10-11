package module

import (
	"elbot/internal/config"
	"elbot/internal/media"
	"elbot/internal/platform/builtin"
	"elbot/internal/storage"
)

// ModuleDeps lists adapter configuration and borrowed infrastructure.
type ModuleDeps struct {
	ConfigPath         string
	Platform           config.PlatformConfig
	Superadmins        map[string][]string
	Prefixes           []string
	SandboxRoot        string
	Files              config.PlatformFilesConfig
	Mode               builtin.Mode
	Store              storage.Store
	ChatHistory        storage.ChatHistoryRepository
	Media              *media.Manager
	MediaRetentionDays int // Retention for platform-delivered attachments.
}
