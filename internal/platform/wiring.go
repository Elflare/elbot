// wiring.go 拆分完整配置、构造内部组件并连接协作能力。

package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"elbot/internal/command"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/platform/dispatch"
	"elbot/internal/platform/headless"
	"elbot/internal/platform/notification"
	"elbot/internal/platform/refcontext"
	"elbot/internal/platform/share"
)

func (m *Supervisor) wire(cfg *config.Config, deps SupervisorDeps, entries []registration) error {
	m.prepare = func() error {
		if !m.agentConnected {
			return errors.New("platform Agent is not connected")
		}
		return m.agentErr
	}
	for _, entry := range entries {
		if err := m.registerPlatform(entry); err != nil {
			return err
		}
	}
	if deps.Mode != "tui" {
		adapter := headless.New()
		m.primary = adapter
		m.ownRuntime(adapter, false, nil, headless.ValidateNotificationTarget)
		m.outputWiring = append(m.outputWiring, func() error {
			return m.connectNotifications(adapter, headless.ValidateNotificationTarget)
		})
	}
	// 引用恢复只在根部连接；适配器不导入 refcontext 兄弟包。
	prefixes := cfg.Commands.Prefixes
	parseCommand := func(text string) command.Parsed { return command.Parse(text, prefixes) }
	referenceDeps := refcontext.Options{
		ParseCommand:          parseCommand,
		FindByPlatformMessage: deps.Store.Messages().FindByPlatformMessage,
		GetSession:            deps.Store.Sessions().Get,
		ListBySession:         deps.Store.Messages().ListBySession,
		FindHistory:           deps.Store.Media().FindHistory,
		GetMedia:              deps.Store.Media().Get,
		FindOutputs:           deps.Store.Media().FindOutputs,
		ChatHistory:           deps.ChatHistory,
	}
	for _, entry := range m.registered {
		if err := entry.build(m, cfg, deps, referenceDeps); err != nil {
			return err
		}
	}
	m.dispatcher = dispatch.New(dispatch.Options{Primary: m.primary, SaveMediaOutput: deps.Store.Media().SaveOutput, Media: deps.Media, PublishUsageFinished: publishMediaUsageFinished(deps.Media), MediaRetentionDays: cfg.Maintenance.SandboxCleanup.RetentionDays})
	var err error
	m.notifications, err = notification.New(m.primary.Name())
	if err != nil {
		return err
	}
	for _, connect := range m.outputWiring {
		if err := connect(); err != nil {
			return err
		}
	}
	// 配置只在构造时读取，运行期故障观察不持有可变配置。
	superadmins := make(map[string][]string, len(cfg.Security.Superadmins))
	for name, ids := range cfg.Security.Superadmins {
		superadmins[name] = slices.Clone(ids)
	}
	m.onFailure = func(name string, failure error) {
		ctx := context.WithoutCancel(m.ctx)
		_, _ = events.EmitLogAsync(ctx, events.LogRecord{Category: events.LogRuntime, Level: slog.LevelError, EventName: "platform_failed", Module: "platform", Type: events.ContentText, Content: fmt.Sprintf("%s: %v", name, failure), ResultStatus: events.ResultFailed})
		m.mu.Lock()
		var targets []events.NotificationTarget
		for _, runtime := range m.runtimes {
			if !runtime.running || runtime.name == name {
				continue
			}
			seen := make(map[string]bool)
			for _, id := range superadmins[runtime.name] {
				id = strings.TrimSpace(id)
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				targets = append(targets, events.NotificationTarget{Platform: runtime.name, Kind: contextinfo.ConversationPrivate, ID: id})
			}
		}
		m.mu.Unlock()
		m.notifications.NotifyPlatformFailure(ctx, name, failure, targets)
	}
	return nil
}

func (m *Supervisor) ownRuntime(adapter interface {
	share.Runtime
	share.NotificationReceiver
}, active bool, waitExit func(context.Context) error, validate func(events.NotificationTarget) error) {
	m.adapters = append(m.adapters, adapter)
	if !active {
		return
	}
	m.runtimes = append(m.runtimes, &platformLifecycle{
		name: adapter.Name(), waitExit: waitExit,
		start:      func(ctx context.Context) error { return adapter.Start(ctx, m.handleMessage) },
		beginClose: adapter.BeginClose, wait: adapter.Wait,
	})
	m.outputWiring = append(m.outputWiring, func() error {
		m.dispatcher.RegisterPlatform(adapter)
		return m.connectNotifications(adapter, validate)
	})
}

func (m *Supervisor) connectNotifications(adapter interface {
	Name() string
	share.NotificationReceiver
}, validate func(events.NotificationTarget) error) error {
	queue, err := m.notifications.AddPlatform(adapter.Name(), validate)
	if err != nil {
		return err
	}
	connection, err := adapter.ConnectNotifications(queue)
	if connection != nil {
		m.connections = append(m.connections, connection)
	}
	return err
}

// configureCloseSteps 在构造及 ConnectAgent 后调用，启动后不再改变资源列表。
func (m *Supervisor) configureCloseSteps() {
	m.closeSteps = append([]closeStep(nil), m.agentCloseSteps...)
	if m.notifications != nil {
		m.closeSteps = append(m.closeSteps, closeStep{"notifications", m.notifications.Close})
	}
	for _, connection := range m.connections {
		m.closeSteps = append(m.closeSteps, closeStep{"notification receiver", connection.Close})
	}
	for _, adapter := range m.adapters {
		m.closeSteps = append(m.closeSteps, closeStep{adapter.Name(), adapter.Close})
	}
}
