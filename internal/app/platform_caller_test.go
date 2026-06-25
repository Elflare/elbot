package app

import (
	"context"
	"encoding/json"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/elvena"
	"elbot/internal/platform"
)

type fakeRuntimeWithCaller struct {
	name string
}

func (r fakeRuntimeWithCaller) Name() string { return r.name }

func (r fakeRuntimeWithCaller) Run(context.Context, platform.PlatformHandler) error { return nil }

func (r fakeRuntimeWithCaller) SendChat(context.Context, delivery.Output) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}

func (r fakeRuntimeWithCaller) SendNotice(context.Context, delivery.Target, delivery.Output) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}

func (r fakeRuntimeWithCaller) CallPlatformAPI(context.Context, string, map[string]any) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func TestPlatformCallerResolverMatchesRuntimeName(t *testing.T) {
	resolver := platformCallerResolver{runtimes: []platformRuntime{fakeRuntimeWithCaller{name: "qqonebot"}}}
	caller, ok := resolver.PlatformCaller("qqonebot")
	if !ok || caller == nil {
		t.Fatalf("PlatformCaller did not find qqonebot caller")
	}
	if _, ok := caller.(elvena.PlatformAPICaller); !ok {
		t.Fatalf("caller does not implement elvena.PlatformAPICaller")
	}
}

func TestPlatformCallerResolverRejectsOtherPlatform(t *testing.T) {
	resolver := platformCallerResolver{runtimes: []platformRuntime{fakeRuntimeWithCaller{name: "qqonebot"}}}
	caller, ok := resolver.PlatformCaller("telegram")
	if ok || caller != nil {
		t.Fatalf("PlatformCaller found unexpected caller: %#v", caller)
	}
}
