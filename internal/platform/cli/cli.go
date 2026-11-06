// Package cli 是 CLI 平台入口，负责对外能力与本地、远程实现的装配。
package cli

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	clishare "elbot/internal/platform/cli/share"
	platformshare "elbot/internal/platform/share"
)

func (a *Adapter) Start(ctx context.Context, handler platformshare.MessageHandler) error {
	if err := a.runtime.Start(ctx, handler); err != nil {
		return err
	}
	_, _ = events.PlatformConnected.Emit(ctx, events.PlatformConnectedEvent{Platform: a.Name()})
	return nil
}

func (a *Adapter) BeginClose()                     { a.runtime.BeginClose() }
func (a *Adapter) Wait(ctx context.Context) error  { return a.runtime.Wait(ctx) }
func (a *Adapter) Close(ctx context.Context) error { return a.runtime.Close(ctx) }

func (a *Adapter) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	if target.Empty() {
		if info, ok := contextinfo.ConversationFromContext(ctx); ok {
			if display, ok := info.PlatformData.(clishare.DisplayTarget); ok && display != nil {
				return delivery.Receipt{}, display.SendReply(ctx, outputs)
			}
		}
	}
	if platformName := strings.TrimSpace(target.Platform); platformName != "" && platformName != a.Name() {
		return delivery.Receipt{}, fmt.Errorf("cli cannot send to platform %q", platformName)
	}
	return a.runtime.SendTo(ctx, target, outputs)
}

func (a *Adapter) StartStream(ctx context.Context) (delivery.MessageStream, error) {
	if info, ok := contextinfo.ConversationFromContext(ctx); ok {
		if display, ok := info.PlatformData.(clishare.DisplayTarget); ok && display != nil {
			return display.StartStream(ctx)
		}
	}
	streamRuntime, ok := a.runtime.(clishare.StreamRuntime)
	if !ok {
		return nil, fmt.Errorf("cli message stream is unavailable")
	}
	return streamRuntime.StartStream(ctx)
}
