// wiring.go 拆分完整配置、构造内部组件并连接协作能力。

package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform/cli"
	"elbot/internal/platform/dispatch"
	"elbot/internal/platform/headless"
	"elbot/internal/platform/notification"
	qqonebot "elbot/internal/platform/qq-onebot"
	"elbot/internal/platform/qqofficial"
	"elbot/internal/platform/refcontext"
	"elbot/internal/platform/share"
	"elbot/internal/platform/telegram"
	"elbot/internal/storage/sqlite"
)

func (m *Supervisor) wire(cfg *config.Config, deps SupervisorDeps) error {
	m.prepare = func() error {
		if !m.agentConnected {
			return errors.New("platform Agent is not connected")
		}
		return m.agentErr
	}
	var err error
	m.historyStore, err = sqlite.NewChatHistory(m.ctx, cfg.Storage.ChatHistorySQLitePath)
	if err != nil {
		return err
	}
	m.history = m.historyStore.Repository()
	if deps.Mode == "tui" {
		m.primary = cli.New()
		m.ownRuntime(m.primary, true, true)
	} else {
		m.primary = headless.New()
		m.ownRuntime(m.primary, false, false)
		cliConfig, err := cli.NewConfigFromPlatformConfig(cfg.Platform["cli"])
		if err != nil {
			return err
		}
		if cliConfig.Enabled && cliConfig.Server.Enabled {
			adapter, err := cli.NewRemoteServer(cli.RemoteServerOptions{Config: cliConfig, ConfigDir: filepath.Dir(cfg.ConfigPath), Superadmins: cfg.Security.Superadmins["cli"]})
			if adapter != nil {
				m.ownRuntime(adapter, err == nil, false)
			}
			if err != nil {
				return err
			}
		}
	}
	// 引用恢复只在根部连接；适配器不导入 refcontext 兄弟包。
	referenceDeps := refcontext.Options{Store: deps.Store, ChatHistory: m.history}
	adapterDeps := func(name string) share.AdapterDeps {
		return share.AdapterDeps{
			Store: deps.Store, ChatHistory: m.history,
			Superadmins: cfg.Security.Superadmins[name], CommandPrefixes: cfg.Commands.Prefixes,
			ConfigDir: filepath.Dir(cfg.ConfigPath), AttachmentDir: filepath.Join(cfg.Sandbox.Root, "platform", name),
			MaxReceiveFileBytes: cfg.PlatformFiles.MaxReceiveFileBytes, DownloadTimeoutSecs: cfg.PlatformFiles.DownloadTimeoutSecs,
			ReferenceResolver: func(ctx context.Context, opts share.ReferenceOptions) (share.ReferenceResult, error) {
				return refcontext.Apply(ctx, referenceDeps, opts)
			},
		}
	}
	if raw, ok := cfg.Platform["qqofficial"]; ok {
		adapter, err := qqofficial.NewFromPlatformConfig(raw, adapterDeps("qqofficial"))
		if adapter != nil {
			m.ownRuntime(adapter, err == nil && adapter.Enabled(), false)
		}
		if err != nil {
			return err
		}
	}
	if raw, ok := cfg.Platform["qqonebot"]; ok {
		adapter, err := qqonebot.NewFromPlatformConfig(raw, adapterDeps("qqonebot"))
		if adapter != nil {
			m.ownRuntime(adapter, err == nil && adapter.Enabled(), false)
		}
		if err != nil {
			return err
		}
	}
	if raw, ok := cfg.Platform["telegram"]; ok {
		adapter, err := telegram.NewFromPlatformConfig(raw, adapterDeps("telegram"))
		if adapter != nil {
			m.ownRuntime(adapter, err == nil && adapter.Enabled(), false)
		}
		if err != nil {
			return err
		}
	}
	m.dispatcher = dispatch.New(dispatch.Options{Primary: m.primary, Store: deps.Store, Media: deps.Media, MediaRetentionDays: cfg.Maintenance.SandboxCleanup.RetentionDays})
	m.notifications, err = notification.New(m.primary.Name())
	if err != nil {
		return err
	}
	for _, runtime := range m.runtimes {
		for _, adapter := range m.adapters {
			if adapter.Name() != runtime.name {
				continue
			}
			var sender interface {
				Name() string
				delivery.MessageSender
			} = adapter
			m.dispatcher.RegisterPlatform(sender)
			receiver, ok := adapter.(share.NotificationReceiver)
			if !ok {
				return fmt.Errorf("platform %q has no notification receiver", adapter.Name())
			}
			queue, err := m.notifications.AddPlatform(adapter.Name())
			if err != nil {
				return err
			}
			connection, err := receiver.ConnectNotifications(queue)
			if connection != nil {
				m.connections = append(m.connections, connection)
			}
			if err != nil {
				return err
			}
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

func (m *Supervisor) ownRuntime(adapter share.Runtime, active, stopApp bool) {
	m.adapters = append(m.adapters, adapter)
	if !active {
		return
	}
	m.runtimes = append(m.runtimes, &platformLifecycle{
		name: adapter.Name(), stopApp: stopApp,
		start:      func(ctx context.Context) error { return adapter.Start(ctx, m.handleMessage) },
		beginClose: adapter.BeginClose, wait: adapter.Wait,
	})
}

// configureCloseSteps 在构造及 ConnectAgent 后调用，启动后不再改变资源列表。
func (m *Supervisor) configureCloseSteps() {
	m.closeSteps = nil
	if m.ui != nil {
		m.closeSteps = append(m.closeSteps, closeStep{"ui", m.ui.Close})
	}
	if m.notifications != nil {
		m.closeSteps = append(m.closeSteps, closeStep{"notifications", m.notifications.Close})
	}
	for _, connection := range m.connections {
		m.closeSteps = append(m.closeSteps, closeStep{"notification receiver", connection.Close})
	}
	for _, adapter := range m.adapters {
		m.closeSteps = append(m.closeSteps, closeStep{adapter.Name(), adapter.Close})
	}
	if m.historyStore != nil {
		m.closeSteps = append(m.closeSteps, closeStep{"chat history", func(context.Context) error { return m.historyStore.Close() }})
	}
}
