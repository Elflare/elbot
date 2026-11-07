package cli

import (
	"fmt"

	"elbot/internal/platform/cli/local"
	"elbot/internal/platform/cli/remote"
	clishare "elbot/internal/platform/cli/share"
	platformshare "elbot/internal/platform/share"
)

// wiring.go 在 CLI 根包内构造并接线 local/remote 实现。

// RemoteServerOptions 是平台注册层交给 CLI 根包的远程服务配置。
type RemoteServerOptions struct {
	Media        clishare.Media
	FrontendDir  string
	PlatformName string
	Config       Config
	ConfigEnv    func(string) (string, bool, error)
	Superadmins  *platformshare.Superadmins
}

// New 创建本地终端实现并以 CLI 平台门面对外提供。
func New(platformName, clientID string) *Adapter {
	runtime := local.New(platformName, clientID)
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
