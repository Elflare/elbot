package cli

import (
	"context"
	"errors"
	"time"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/signal"
)

// messageData identifies the original CLI destination. Background conversations
// have no CLI destination, even when their public platform name is "cli".
type messageData struct {
	client  *remoteClientConn // Original remote connection; never replaced by the current client.
	adapter *Adapter          // Local terminal that accepted the input.
}

func uiTarget(ctx context.Context) messageData {
	info, _ := contextinfo.ConversationFromContext(ctx)
	data, _ := info.PlatformData.(messageData)
	return data
}

func (d messageData) setRuntimeStatus(ctx context.Context, snapshot runtimestatus.Snapshot) error {
	if d.client != nil {
		_, err := d.client.write(ctx, remoteMessage{Type: remoteMsgStatus, Snapshot: snapshot})
		return err
	}
	return d.adapter.sendTUIMessage(tuiStatusMsg(snapshot), FormatCompact(snapshot, time.Now(), 0))
}

// UI owns CLI state subscriptions and their asynchronous display worker.
type UI struct {
	display     *statusDisplay
	connections []*signal.Connection
}

// ConnectUI is called after construction and before any platform accepts input.
func ConnectUI(status *signal.Signal[agentevents.StatusChangedEvent, signal.Empty], binding *signal.Signal[session.BindingChangedEvent, signal.Empty]) (*UI, error) {
	u := &UI{display: newStatusDisplay()}
	c, err := status.Observe(u.display.receive, signal.ConnectOptions{})
	if err == nil {
		u.connections = append(u.connections, c)
		c, err = binding.Observe(u.display.forget, signal.ConnectOptions{})
	}
	if err != nil {
		return nil, errors.Join(err, u.Close(context.Background()))
	}
	u.connections = append(u.connections, c)
	return u, nil
}

func (u *UI) BeginClose() {
	for _, c := range u.connections {
		c.BeginClose()
	}
	u.display.BeginClose()
}

func (u *UI) Close(ctx context.Context) error {
	u.BeginClose()
	var errs []error
	for _, c := range u.connections {
		errs = append(errs, c.Close(ctx))
	}
	return errors.Join(append(errs, u.display.Close(ctx))...)
}
