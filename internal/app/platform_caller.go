package app

import (
	"strings"

	"elbot/internal/elvena"
)

type platformCallerResolver struct {
	runtimes []platformRuntime
}

func (r platformCallerResolver) PlatformCaller(name string) (elvena.PlatformAPICaller, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	for _, runtime := range r.runtimes {
		if runtime == nil || runtime.Name() != name {
			continue
		}
		caller, ok := runtime.(elvena.PlatformAPICaller)
		return caller, ok
	}
	return nil, false
}
