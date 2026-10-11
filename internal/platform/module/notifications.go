package module

import (
	"context"
	"log/slog"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/notification"
	"elbot/internal/platform"
	"fmt"
)

func (m *Module) notifyPlatformFailure(ctx context.Context, name string, failure error) {
	m.mu.Lock()
	var targets []events.NotificationTarget
	for platformName := range m.running {
		if platformName == name {
			continue
		}
		seen := make(map[string]bool)
		for _, id := range m.superadmins[platformName] {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			targets = append(targets, events.NotificationTarget{Platform: platformName, Kind: contextinfo.ConversationPrivate, ID: id})
		}
	}
	m.mu.Unlock()
	for _, target := range targets {
		_, _ = events.NotificationRequested.EmitAsync(ctx, events.Notification{
			Target: target, Level: slog.LevelError,
			Text: fmt.Sprintf("平台 %s 异常退出：%v", name, failure),
		})
	}
}

func (m *Module) connectNotifications() error {
	manager, err := notification.New(m.primary.Name())
	if err != nil {
		return err
	}
	m.notifications = manager
	for _, adapter := range m.runtimes {
		receiver, ok := adapter.(platform.NotificationReceiver)
		if !ok {
			return fmt.Errorf("platform %q has no notification receiver", adapter.Name())
		}
		s, err := manager.AddPlatform(adapter.Name())
		if err != nil {
			return err
		}
		if _, err = receiver.ConnectNotifications(s); err != nil {
			return err
		}
	}
	return nil
}
