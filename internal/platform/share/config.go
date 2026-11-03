// config.go 提供配置解析和适配器共享配置。

package share

import (
	"fmt"

	"github.com/pelletier/go-toml/v2"
)

// CommonConfig 是根部为各平台提取的公共配置，不包含存储或运行服务。
type CommonConfig struct {
	PlatformName string                             // 根部登记的平台身份。
	Superadmins  *Superadmins                       // 当前平台的超级管理员能力，由根部构造。
	ConfigEnv    func(string) (string, bool, error) // 根部绑定配置位置的环境读取能力。
}

// DecodeConfig decodes a raw [platform.<name>] app.toml section into an adapter-owned config struct.
func DecodeConfig(raw map[string]any, out any) error {
	if out == nil {
		return fmt.Errorf("platform config output is nil")
	}
	if raw == nil {
		return nil
	}
	data, err := toml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal platform config: %w", err)
	}
	if err := toml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode platform config: %w", err)
	}
	return nil
}
