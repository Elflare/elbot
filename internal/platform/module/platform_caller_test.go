package module

import (
	"context"
	"encoding/json"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/elvena"
	"elbot/internal/events"
	"elbot/internal/platform"
)

type fakeRuntimeWithCaller struct {
	name string
}

func (r fakeRuntimeWithCaller) Name() string { return r.name }

func (r fakeRuntimeWithCaller) Run(context.Context, platform.PlatformHandler) error { return nil }

func (r fakeRuntimeWithCaller) SendChat(context.Context, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func (r fakeRuntimeWithCaller) SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func (r fakeRuntimeWithCaller) CallPlatformAPI(context.Context, string, map[string]any) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func TestPlatformCallerResolverMatchesRuntimeName(t *testing.T) {
	resolver := platformCallerResolver{runtimes: []platform.Runtime{fakeRuntimeWithCaller{name: "qqonebot"}}}
	caller, ok := resolver.PlatformCaller("qqonebot")
	if !ok || caller == nil {
		t.Fatalf("PlatformCaller did not find qqonebot caller")
	}
	if _, ok := caller.(elvena.PlatformAPICaller); !ok {
		t.Fatalf("caller does not implement elvena.PlatformAPICaller")
	}
}

func TestPlatformCallerResolverRejectsOtherPlatform(t *testing.T) {
	resolver := platformCallerResolver{runtimes: []platform.Runtime{fakeRuntimeWithCaller{name: "qqonebot"}}}
	caller, ok := resolver.PlatformCaller("telegram")
	if ok || caller != nil {
		t.Fatalf("PlatformCaller found unexpected caller: %#v", caller)
	}
}
