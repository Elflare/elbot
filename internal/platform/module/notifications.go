package module

import (
	"elbot/internal/notification"
	"elbot/internal/platform"
	"fmt"
)

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
