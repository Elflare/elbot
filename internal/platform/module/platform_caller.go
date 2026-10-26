// platform_caller.go 保留旧平台原生调用对象查找入口。
// TODO：按职责迁入public_api.go 与内部调用路由并更新调用方后删除本文件。

package module

import (
	"strings"

	"elbot/internal/elvena"
	"elbot/internal/platform"
)

type platformCallerResolver struct {
	runtimes []platform.Runtime
}

func (r platformCallerResolver) PlatformCaller(name string) (elvena.PlatformAPICaller, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	for _, runtime := range r.runtimes {
		if runtime.Name() != name {
			continue
		}
		caller, ok := runtime.(elvena.PlatformAPICaller)
		return caller, ok
	}
	return nil, false
}

func (m *Module) PlatformCaller(name string) (elvena.PlatformAPICaller, bool) {
	return platformCallerResolver{runtimes: m.runtimes}.PlatformCaller(name)
}
