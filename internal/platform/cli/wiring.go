package cli

import (
	"fmt"

	"elbot/internal/platform/cli/local"
	"elbot/internal/platform/cli/remote"
)

// wiring.go 在 CLI 根包内构造并接线 local/remote 实现。

// New 创建本地终端实现并以 CLI 平台门面对外提供。
func New(platformName string) *Adapter {
	runtime := local.New(platformName)
	return &Adapter{name: platformName, runtime: runtime}
}

// NewRemoteServer 创建远程服务实现并以 CLI 平台门面对外提供。
func NewRemoteServer(opts RemoteServerOptions) (*Adapter, error) {
	instance, err := remote.NewServer(remote.ServerOptions{
		Config: opts.Config, PlatformName: opts.PlatformName, ConfigEnv: opts.ConfigEnv,
		Superadmins: opts.Superadmins, FrontendDir: opts.FrontendDir, Media: opts.Media,
	})
	if err != nil {
		return nil, fmt.Errorf("create CLI remote server: %w", err)
	}
	return &Adapter{name: opts.PlatformName, runtime: instance}, nil
}
