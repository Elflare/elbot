// ui.go 接入 Agent 状态事件并管理根部状态协调生命周期。
package cli

import (
	"context"

	"elbot/internal/signal"
)

// AgentEvents 是 CLI 根部消费的 Agent 状态与绑定事实。
type AgentEvents interface {
	ObserveStatusChanged(func(StatusChangedEvent), signal.ConnectOptions) (*signal.Connection, error)
	ObserveBindingChanged(func(BindingChangedEvent), signal.ConnectOptions) (*signal.Connection, error)
}

// UI 持有 CLI 根部状态协调器和事件订阅。
type UI struct {
	display     *statusDisplay
	connections []*signal.Connection
}

// ConnectUI 在平台开始接收输入前建立状态订阅。
func (a *Adapter) ConnectUI(source AgentEvents) (*UI, error) {
	u := &UI{display: newStatusDisplay(a.name)}
	connection, err := source.ObserveStatusChanged(u.display.receive, signal.ConnectOptions{})
	if connection != nil {
		u.connections = append(u.connections, connection)
	}
	if err == nil {
		connection, err = source.ObserveBindingChanged(func(event BindingChangedEvent) {
			u.display.forget(event)
			old, next := event.Old, event.New
			if old == nil || next == nil || old.Scope.Platform != a.name {
				return
			}
			if remote, ok := a.runtime.(interface{ PresentBindingChange(string, string, string) }); ok {
				remote.PresentBindingChange(old.Scope.ChatID, old.SessionID, next.SessionID)
			}
		}, signal.ConnectOptions{})
		if connection != nil {
			u.connections = append(u.connections, connection)
		}
	}
	return u, err
}

func (u *UI) BeginClose() {
	for _, connection := range u.connections {
		connection.BeginClose()
	}
	u.display.BeginClose()
}

func (u *UI) Close(ctx context.Context) error {
	u.BeginClose()
	for _, connection := range u.connections {
		if err := connection.Close(ctx); err != nil {
			return err
		}
	}
	return u.display.Close(ctx)
}
