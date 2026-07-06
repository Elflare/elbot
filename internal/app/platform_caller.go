package app

import (
	"strings"

	"elbot/internal/elvena"
	"elbot/internal/hook/rules"
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

type hookPlatformCallerResolver struct {
	runtimes []platformRuntime
}

func (r hookPlatformCallerResolver) PlatformCaller(name string) (rules.PlatformAPICaller, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	for _, runtime := range r.runtimes {
		if runtime == nil || runtime.Name() != name {
			continue
		}
		caller, ok := runtime.(rules.PlatformAPICaller)
		return caller, ok
	}
	return nil, false
}
