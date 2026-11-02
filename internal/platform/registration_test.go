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
	"elbot/internal/platform/headless"
	"elbot/internal/platform/refcontext"
	"elbot/internal/platform/share"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/testutil"
)

func testRegistration() registration {
	return registration{
		definition: share.Definition{
			Name: "future", PrivateSessionIDCode: "fx", GroupSessionIDCode: "room",
			ConfigDefinition:           config.Definition{Asset: "app.toml", Node: []string{"platform", "future"}, New: func() any { return &struct{}{} }},
			ValidateNotificationTarget: share.ValidateNotificationTarget,
		},
		build: func(*Supervisor, *config.Config, SupervisorDeps, refcontext.Options) error { return nil },
	}
}

func TestRegistrationRejectsIncompleteAndDuplicateDeclarations(t *testing.T) {
	cases := []struct {
		name   string
		change func(*registration)
	}{
		{"name", func(r *registration) { r.definition.Name = " " }},
		{"private code", func(r *registration) { r.definition.PrivateSessionIDCode = "" }},
		{"group code", func(r *registration) { r.definition.GroupSessionIDCode = " " }},
		{"config", func(r *registration) { r.definition.ConfigDefinition.New = nil }},
		{"validator", func(r *registration) { r.definition.ValidateNotificationTarget = nil }},
		{"constructor", func(r *registration) { r.build = nil }},
		{"duplicate name", func(r *registration) {
			r.definition.Name = "future"
			r.definition.PrivateSessionIDCode, r.definition.GroupSessionIDCode = "other", "another"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Supervisor{}
			if err := m.registerPlatform(testRegistration()); err != nil {
				t.Fatal(err)
			}
			entry := testRegistration()
			entry.definition.Name = "another"
			entry.definition.PrivateSessionIDCode, entry.definition.GroupSessionIDCode = "private", "group"
			tc.change(&entry)
			if err := m.registerPlatform(entry); err == nil {
				t.Fatalf("registration error = %v", err)
			}
			if len(m.registered) != 1 {
				t.Fatal("invalid declaration was published")
			}
		})
	}
}

func TestRegistrationCodeCollisions(t *testing.T) {
	for _, codes := range [][2]string{{"fx", "other"}, {"room", "other"}, {"other", "fx"}, {"other", "room"}} {
		t.Run(strings.Join(codes[:], "/"), func(t *testing.T) {
			m := &Supervisor{}
			if err := m.registerPlatform(testRegistration()); err != nil {
				t.Fatal(err)
			}
			entry := testRegistration()
			entry.definition.Name = "another"
			entry.definition.PrivateSessionIDCode, entry.definition.GroupSessionIDCode = codes[0], codes[1]
			if err := m.registerPlatform(entry); err == nil || len(m.registered) != 1 {
				t.Fatalf("collision accepted: registrations=%d error=%v", len(m.registered), err)
			}
		})
	}
	m := &Supervisor{}
	entry := testRegistration()
	entry.definition.GroupSessionIDCode = entry.definition.PrivateSessionIDCode
	if err := m.registerPlatform(entry); err != nil {
		t.Fatalf("same platform may share a code: %v", err)
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
	entry.build = func(m *Supervisor, _ *config.Config, _ SupervisorDeps, _ refcontext.Options) error {
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
			entry.build = func(m *Supervisor, _ *config.Config, _ SupervisorDeps, _ refcontext.Options) error {
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
