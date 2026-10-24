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
