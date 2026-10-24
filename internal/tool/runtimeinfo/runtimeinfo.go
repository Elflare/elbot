// 描述并规范化工具使用的运行环境信息。
package runtimeinfo

import (
	"path/filepath"
	"time"

	"elbot/internal/config"
)

type Info struct {
	ConfigDir   string
	ConfigPath  string
	SandboxRoot string
	Now         func() time.Time
}

func First(values ...Info) Info {
	if len(values) == 0 {
		return Info{}.Normalize()
	}
	return values[0].Normalize()
}

func (i Info) Normalize() Info {
	if i.ConfigDir == "" && i.ConfigPath != "" {
		i.ConfigDir = filepath.Dir(filepath.Clean(i.ConfigPath))
	}
	if i.ConfigPath == "" && i.ConfigDir != "" {
		i.ConfigPath = filepath.Join(i.ConfigDir, "app.toml")
	}
	if i.SandboxRoot == "" {
		i.SandboxRoot = config.Default().Sandbox.Root
	}
	if i.Now == nil {
		i.Now = time.Now
	}
	return i
}

func (i Info) CurrentTime() time.Time {
	return i.Now()
}
