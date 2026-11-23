package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	clishare "elbot/internal/platform/cli/share"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/signal"
)

type bindingChangeRecorder struct {
	clishare.Runtime
	changes [][3]string
}

func (r *bindingChangeRecorder) PresentBindingChange(account, old, next string) {
	r.changes = append(r.changes, [3]string{account, old, next})
}

func TestConnectUIForwardsOnlyBindingReplacementsOnOwnPlatform(t *testing.T) {
	recorder := &bindingChangeRecorder{}
	source := testAgentEvents{signal.New[StatusChangedEvent, signal.Empty]("test.status"), signal.New[BindingChangedEvent, signal.Empty]("test.binding")}
	defer source.status.Close(t.Context())
	defer source.binding.Close(t.Context())
	u, err := (&Adapter{name: testPlatformName, runtime: recorder}).ConnectUI(source)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close(t.Context())
	scope := session.Scope{Platform: testPlatformName, ChatID: "user"}
	a, b := &BindingSnapshot{Scope: scope, SessionID: "A"}, &BindingSnapshot{Scope: scope, SessionID: "B"}
	foreign := scope
	foreign.Platform = "other"
	for i, event := range []BindingChangedEvent{{New: a}, {Old: a, New: b}, {Old: b}, {Old: &BindingSnapshot{Scope: foreign, SessionID: "A"}, New: &BindingSnapshot{Scope: foreign, SessionID: "B"}}} {
		event.Version = uint64(i + 1)
		if _, err := source.binding.Emit(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	if len(recorder.changes) != 1 || recorder.changes[0] != [3]string{"user", "A", "B"} {
		t.Fatalf("changes = %v", recorder.changes)
	}
}
func TestStatusBindingChangeFinishesOldSendAndRejectsLateEvents(t *testing.T) {
	d, stop := startTestDisplay(t)
	scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "user"}, Platform: "cli", ChatID: "local", ChatType: "private"}
	old := &BindingSnapshot{Scope: scope, SessionID: "same", Version: 1}
	next := &BindingSnapshot{Scope: scope, SessionID: "same", Version: 2}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	displayed := make(chan string, 4)
	d.send = func(ctx context.Context, _ clishare.DisplayTarget, snapshot runtimestatus.Snapshot) error {
		if snapshot.Provider == "old" {
			close(entered)
			<-release
			if ctx.Err() != nil {
				t.Error("binding change canceled in-flight send", ctx.Err())
			}
		}
		displayed <- snapshot.Provider
		return nil
	}
	conversation := &contextinfo.Conversation{PlatformData: testDisplayTarget{id: "one"}}
	event := StatusChangedEvent{Conversation: conversation, Binding: old, Version: 100, Snapshot: runtimestatus.Snapshot{SessionID: "same", Provider: "old", Phase: runtimestatus.PhaseLLM}}
	d.receive(event)
	<-entered
	event.Version, event.Snapshot.Provider = 101, "old pending"
	d.receive(event)
	d.forget(BindingChangedEvent{Version: 2, Old: old, New: next})
	// 状态版本和绑定版本各自独立，新激活不沿用旧状态的版本水位。
	event.Binding, event.Version, event.Snapshot.Provider = next, 1, "new"
	event.Snapshot.Phase = runtimestatus.PhaseDone
	d.receive(event)
	once.Do(func() { close(release) })
	for _, want := range []string{"old", "new"} {
		select {
		case got := <-displayed:
			if got != want {
				t.Fatalf("display = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("ordered status did not display")
		}
	}
	d.forget(BindingChangedEvent{Version: 1, New: old})
	event.Binding, event.Version, event.Snapshot.Provider = old, 101, "late old"
	d.receive(event)
	d.forget(BindingChangedEvent{Version: 3, Old: next})
	event.Binding, event.Version, event.Snapshot.Provider = next, 2, "late unbound"
	d.receive(event)
	stop()
	if len(displayed) != 0 {
		t.Fatal("stale binding resurrected")
	}
}

func TestStatusTerminalWatermarkOutlivesPendingBody(t *testing.T) {
	d, _ := startTestDisplay(t)
	sent := make(chan struct{}, 2)
	d.send = func(context.Context, clishare.DisplayTarget, runtimestatus.Snapshot) error {
		sent <- struct{}{}
		return nil
	}
	event := StatusChangedEvent{Conversation: &contextinfo.Conversation{PlatformData: testDisplayTarget{id: "one"}}, Version: 10, Snapshot: runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseDone}}
	d.receive(event)
	<-sent
	event.Version, event.Snapshot.Phase = 9, runtimestatus.PhaseLLM
	d.receive(event)
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.pending) != 0 || d.versions[statusTarget{SessionID: "s", DisplayID: "one"}] != 10 {
		t.Fatal("terminal body retained or watermark lost")
	}
}
