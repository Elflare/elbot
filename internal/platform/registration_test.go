package platform

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/agent"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/platform/cli"
	"elbot/internal/platform/headless"
	qqonebot "elbot/internal/platform/qq-onebot"
	"elbot/internal/platform/qqofficial"
	"elbot/internal/platform/refcontext"
	"elbot/internal/platform/share"
	"elbot/internal/platform/telegram"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/testutil"
)

func testRegistration() registration {
	return registration{
		name: "future",
		definition: share.Definition{
			PrivateSessionIDCode: "fx", GroupSessionIDCode: "room",
			ConfigDefinition:           config.Definition{Asset: "app.toml", Node: []string{"platform", "future"}, New: func() any { return &struct{}{} }},
			ValidateNotificationTarget: share.ValidateNotificationTarget,
		},
		build: func(*Supervisor, SupervisorDeps, refcontext.Options, map[string]any, share.CommonConfig) error {
			return nil
		},
	}
}

// 固定清单的开发错误在测试中发现，不在每次启动时重复防御。
func TestRegistrationListIsCompleteAndUnique(t *testing.T) {
	names, codes := map[string]bool{}, map[string]string{}
	for _, entry := range registrations() {
		d := entry.definition
		if entry.name == "" || d.PrivateSessionIDCode == "" || d.GroupSessionIDCode == "" ||
			d.ConfigDefinition.New == nil || d.ValidateNotificationTarget == nil || entry.build == nil {
			t.Fatalf("incomplete registration: %+v", entry)
		}
		if names[entry.name] {
			t.Fatalf("duplicate platform %q", entry.name)
		}
		names[entry.name] = true
		for _, code := range []string{d.PrivateSessionIDCode, d.GroupSessionIDCode} {
			if owner, exists := codes[code]; exists && owner != entry.name {
				t.Fatalf("session code %q shared by %s and %s", code, owner, entry.name)
			}
			codes[code] = entry.name
		}
		if !reflect.DeepEqual(d.ConfigDefinition.Node, []string{"platform", entry.name}) {
			t.Fatalf("config node mismatch: %+v", entry)
		}
	}
}

func TestAdaptersUseInjectedNames(t *testing.T) {
	const name = "injected-platform"
	common := share.CommonConfig{PlatformName: name, Superadmins: share.NewSuperadmins(name, nil), ConfigEnv: (&config.Config{}).ConfigEnv}
	tg, err := telegram.NewFromPlatformConfig(nil, telegram.Deps{CommonConfig: common})
	if err != nil {
		t.Fatal(err)
	}
	qq, err := qqofficial.NewFromPlatformConfig(nil, qqofficial.Deps{CommonConfig: common})
	if err != nil {
		t.Fatal(err)
	}
	ob, err := qqonebot.NewFromPlatformConfig(nil, qqonebot.Deps{CommonConfig: common})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := cli.NewRemoteServer(cli.RemoteServerOptions{
		PlatformName: name, Superadmins: common.Superadmins, ConfigEnv: common.ConfigEnv,
		Config: cli.Config{Server: cli.ServerConfig{Enabled: true, Listen: "127.0.0.1:0", Tokens: map[string][]string{"local": {"TOKEN"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, adapter := range []share.Runtime{tg, qq, ob, remote, cli.New(name), headless.New(name)} {
		if adapter.Name() != name {
			t.Fatalf("adapter %T name=%q", adapter, adapter.Name())
		}
		if err := adapter.Close(t.Context()); err != nil {
			t.Errorf("close %T: %v", adapter, err)
		}
	}
}

func TestDisabledPlatformsRetainCodesAndConfigDefinitions(t *testing.T) {
	m, err := NewSupervisor(t.Context(), &config.Config{Platform: map[string]map[string]any{"telegram": {"enabled": false}}}, SupervisorDeps{Mode: "service", Store: testutil.NewSQLiteStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if len(m.runtimes) != 0 {
		t.Fatal("disabled platform registered ingress")
	}
	for name, codes := range map[string][2]string{"cli": {"cl", "cl"}, "qqofficial": {"qq", "QQ"}, "qqonebot": {"ob", "OB"}, "telegram": {"tg", "TG"}} {
		for _, kind := range []contextinfo.ConversationKind{contextinfo.ConversationCLI, contextinfo.ConversationPrivate, contextinfo.ConversationGroup, contextinfo.ConversationChannel} {
			want := codes[0]
			if kind == contextinfo.ConversationGroup || kind == contextinfo.ConversationChannel {
				want = codes[1]
			}
			if code, err := m.SessionIDCode(name, kind); err != nil || code != want {
				t.Fatalf("%s/%s code=%q want=%q error=%v", name, kind, code, want, err)
			}
		}
	}
	for _, name := range []string{"unknown", "service", ""} {
		if code, err := m.SessionIDCode(name, contextinfo.ConversationPrivate); err == nil || code != "" {
			t.Fatalf("unknown %q code=%q error=%v", name, code, err)
		}
	}
	var nodes []string
	for _, definition := range ConfigDefinitions() {
		nodes = append(nodes, strings.Join(definition.Node, "."))
	}
	if want := []string{"platform.cli", "platform.qqofficial", "platform.qqonebot", "platform.telegram"}; !reflect.DeepEqual(nodes, want) {
		t.Fatalf("config nodes=%v", nodes)
	}
}

type registeredTestRuntime struct {
	headless.Adapter
	name     string
	received chan string
	closed   bool
}

func (r *registeredTestRuntime) Name() string                { return r.name }
func (r *registeredTestRuntime) Close(context.Context) error { r.closed = true; return nil }
func (r *registeredTestRuntime) ConnectNotifications(s *signal.Signal[events.Notification, events.NotificationReceipt]) (*signal.Connection, error) {
	return s.Connect(func(_ context.Context, n events.Notification) (events.NotificationReceipt, error) {
		r.received <- n.Text
		return events.NotificationReceipt{}, nil
	}, signal.ConnectOptions{})
}

type registrationModels struct{}

func (registrationModels) ResolveNaming(context.Context) modelmgr.NamingSelection {
	return modelmgr.NamingSelection{}
}

type registrationMaterials struct{}

func (registrationMaterials) MaterialFor(llm.Origin) (session.MaterialPreparer, error) {
	return session.DisplayMaterial{}, nil
}

func TestNewPlatformFlowsThroughConstructionAgentNotificationsAndSession(t *testing.T) {
	store := testutil.NewSQLiteStore(t)
	runtime := &registeredTestRuntime{name: "future", received: make(chan string, 1)}
	entry := testRegistration()
	connected := 0
	entry.build = func(m *Supervisor, _ SupervisorDeps, _ refcontext.Options, _ map[string]any, _ share.CommonConfig) error {
		m.ownRuntime(runtime, true, nil, entry.definition.ValidateNotificationTarget)
		m.agentWiring = append(m.agentWiring, func(AgentCapabilities) error { connected++; return nil })
		return nil
	}
	m := &Supervisor{}
	m.lifecycle.init(context.Background())
	err := m.wire(&config.Config{}, SupervisorDeps{Mode: "service", Store: store}, append(registrations(), entry))
	m.configureCloseSteps()
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ConnectAgent(nil); err != nil {
		t.Fatal(err)
	}
	if err := m.ConnectAgent(nil); err != nil || connected != 1 {
		t.Fatalf("Agent connected %d times: %v", connected, err)
	}
	sessions, err := session.NewSupervisor(t.Context(), &config.Config{Session: config.SessionConfig{DefaultMode: storage.SessionModeWork}}, session.SupervisorDeps{
		Store: store, Models: registrationModels{}, Materials: registrationMaterials{}, SessionIDCode: m.SessionIDCode,
		ActiveSessionIDs: func() []string { return nil }, CheckForeground: func(context.Context, *storage.Session) error { return nil },
		IsSuperadmin: func(session.Scope) bool { return false }, ReleaseSession: func(string) {}, ReleaseBinding: func(*session.Binding) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sessions.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	scope := session.Scope{ActorID: "owner", Platform: runtime.Name(), PlatformScopeID: "group", ConversationKind: contextinfo.ConversationGroup}
	row, err := sessions.Create(t.Context(), scope, session.CreateRequest{})
	if err != nil || row.ID != "room1" {
		t.Fatalf("session=%+v error=%v", row, err)
	}
	reserved, err := sessions.AllocateID(t.Context(), row)
	if err != nil || reserved != "room2" {
		t.Fatalf("derived ID=%q error=%v", reserved, err)
	}
	scope.ConversationKind = contextinfo.ConversationChannel
	channel, err := sessions.Create(t.Context(), scope, session.CreateRequest{})
	if err != nil || channel.ID != "room3" {
		t.Fatalf("channel=%+v error=%v", channel, err)
	}
	scope.ConversationKind = contextinfo.ConversationPrivate
	private, err := sessions.Create(t.Context(), scope, session.CreateRequest{})
	if err != nil || private.ID != "fx4" {
		t.Fatalf("private=%+v error=%v", private, err)
	}
	background, err := sessions.PrepareBackground(t.Context(), scope, session.BackgroundRequest{Kind: "cron", Name: "task", TargetPlatforms: []string{"future"}})
	if err != nil || background.ID != "crfx5" {
		t.Fatalf("background=%+v error=%v", background, err)
	}
	if _, err := events.NotificationRequested.Emit(t.Context(), events.Notification{Text: "future notice", Target: events.NotificationTarget{Platform: runtime.Name(), ID: "group", Kind: contextinfo.ConversationGroup}}); err != nil {
		t.Fatal(err)
	}
	if got := <-runtime.received; got != "future notice" {
		t.Fatal(got)
	}
	if err := m.Close(context.Background()); err != nil || !runtime.closed {
		t.Fatalf("runtime cleanup=%v closed=%v", err, runtime.closed)
	}
}

func TestRegisteredPartialFailuresRetainCleanup(t *testing.T) {
	for _, phase := range []string{"construction", "Agent connection"} {
		t.Run(phase, func(t *testing.T) {
			fault := errors.New(phase)
			runtime := &registeredTestRuntime{name: "future"}
			entry := testRegistration()
			var released []string
			entry.build = func(m *Supervisor, _ SupervisorDeps, _ refcontext.Options, _ map[string]any, _ share.CommonConfig) error {
				m.ownRuntime(runtime, false, nil, entry.definition.ValidateNotificationTarget)
				if phase == "construction" {
					return fault
				}
				m.agentWiring = append(m.agentWiring, func(AgentCapabilities) error {
					m.agentCloseSteps = append(m.agentCloseSteps, closeStep{"partial connection", func(context.Context) error {
						if runtime.closed {
							t.Error("adapter closed before Agent resources")
						}
						released = append(released, "Agent")
						return nil
					}})
					return fault
				})
				return nil
			}
			m := &Supervisor{}
			m.lifecycle.init(context.Background())
			err := m.wire(&config.Config{}, SupervisorDeps{Mode: "service", Store: testutil.NewSQLiteStore(t)}, []registration{entry})
			m.configureCloseSteps()
			if phase == "Agent connection" {
				if err != nil {
					t.Fatal(err)
				}
				err = m.ConnectAgent(nil)
			}
			if !errors.Is(err, fault) {
				t.Fatalf("partial failure=%v", err)
			}
			if err := m.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !runtime.closed || (phase == "Agent connection" && len(released) != 1) {
				t.Fatalf("closed=%v released=%v", runtime.closed, released)
			}
		})
	}
}

type registrationAgent struct {
	AgentCapabilities
	status       *signal.Signal[agent.StatusChangedEvent, signal.Empty]
	binding      *signal.Signal[agent.BindingChangedEvent, signal.Empty]
	connections  []*signal.Connection
	commandCalls int
}

func (*registrationAgent) Complete(context.Context, completion.Request) ([]completion.Item, error) {
	return nil, nil
}
func (a *registrationAgent) Commands() []command.Info { a.commandCalls++; return nil }
func (a *registrationAgent) ObserveStatusChanged(receive func(agent.StatusChangedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	c, err := a.status.Observe(func(_ context.Context, event agent.StatusChangedEvent) error { return receive(event) }, options)
	a.connections = append(a.connections, c)
	return c, err
}
func (a *registrationAgent) ObserveBindingChanged(receive func(agent.BindingChangedEvent) error, options signal.ConnectOptions) (*signal.Connection, error) {
	c, err := a.binding.Observe(func(_ context.Context, event agent.BindingChangedEvent) error { return receive(event) }, options)
	a.connections = append(a.connections, c)
	return c, err
}

func TestRegisteredCLIAndTelegramAgentConnections(t *testing.T) {
	for _, mode := range []string{"tui", "service"} {
		t.Run(mode, func(t *testing.T) {
			source := &registrationAgent{
				status:  signal.New[agent.StatusChangedEvent, signal.Empty]("test.status", signal.Options[agent.StatusChangedEvent]{}),
				binding: signal.New[agent.BindingChangedEvent, signal.Empty]("test.binding", signal.Options[agent.BindingChangedEvent]{}),
			}
			t.Cleanup(func() { _ = source.status.Close(context.Background()); _ = source.binding.Close(context.Background()) })
			cfg := &config.Config{Platform: map[string]map[string]any{
				"telegram": {"enabled": false},
				"cli":      {"enabled": true, "server": map[string]any{"enabled": true, "tokens": map[string]any{"local": []string{"test-token"}}}},
			}}
			m, err := NewSupervisor(t.Context(), cfg, SupervisorDeps{Mode: mode, Store: testutil.NewSQLiteStore(t)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := m.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			if err := m.ConnectAgent(source); err != nil {
				t.Fatal(err)
			}
			if err := m.ConnectAgent(source); err != nil {
				t.Fatal(err)
			}
			if len(source.connections) != 2 || source.commandCalls != 1 {
				t.Fatalf("connections=%d command calls=%d", len(source.connections), source.commandCalls)
			}
			stream := m.StartReasoning(t.Context())
			if stream == nil {
				t.Fatal("CLI reasoning provider was not connected")
			}
			stream.Finish(t.Context())
			if err := m.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, connection := range source.connections {
				select {
				case <-connection.Done():
				default:
					t.Fatal("CLI state subscription was not released")
				}
			}
		})
	}
}
