package execution

import (
	"context"

	"sync"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/platform/dispatch"
	"elbot/internal/platform/notification"
	"elbot/internal/signal"
)

type fixtureNotificationRouteKey struct{}

type fixtureNotificationHub struct {
	manager *notification.Manager
	mu      sync.Mutex
	routes  map[string]*dispatch.Router
}

var fixtureNotificationHubs sync.Map

// Several restart tests assemble multiple agents inside one application/test.
// They share one global manager, just as the real application does.
func connectFixtureNotifications(t *testing.T, name string, router *dispatch.Router) {
	t.Helper()
	value, ok := fixtureNotificationHubs.Load(t)
	if !ok {
		manager, err := notification.New(name)
		if err != nil {
			t.Fatal(err)
		}
		hub := &fixtureNotificationHub{manager: manager, routes: make(map[string]*dispatch.Router)}
		fixtureNotificationHubs.Store(t, hub)
		t.Cleanup(func() { _ = manager.Close(context.Background()); fixtureNotificationHubs.Delete(t) })
		value = hub
	}
	hub := value.(*fixtureNotificationHub)
	hub.mu.Lock()
	_, exists := hub.routes[name]
	hub.routes[name] = router
	hub.mu.Unlock()
	if exists {
		return
	}
	s, err := hub.manager.AddPlatform(name)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Connect(func(ctx context.Context, n events.Notification) (events.NotificationReceipt, error) {
		route, _ := ctx.Value(fixtureNotificationRouteKey{}).(*dispatch.Router)
		if route == nil {
			hub.mu.Lock()
			route = hub.routes[name]
			hub.mu.Unlock()
		}
		r, _, err := route.SendTo(ctx, platform.NotificationTarget(n), []delivery.Output{delivery.Text(n.Text)})
		return events.NotificationReceipt{PlatformMessageIDs: r.PlatformMessageIDs}, err
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
}
