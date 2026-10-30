// config.go 提供配置解析和适配器共享配置。

package share

import (
	"fmt"

	"github.com/pelletier/go-toml/v2"
)

// CommonConfig 是根部为各平台提取的公共配置，不包含存储或运行服务。
type CommonConfig struct {
	Superadmins         []string // 当前平台的超级管理员标识。
	ConfigDir           string   // 配置与环境文件目录。
	AttachmentDir       string   // 平台附件目录。
	MaxReceiveFileBytes int64    // 入站文件字节上限。
	DownloadTimeoutSecs int      // 协议下载超时秒数。
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
