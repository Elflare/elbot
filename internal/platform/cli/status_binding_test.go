package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
)

func TestStatusBindingChangeFinishesOldSendAndRejectsLateEvents(t *testing.T) {
	d := newStatusDisplay()
	defer d.Close(context.Background())
	scope := session.Scope{ActorID: "user", Platform: "cli", PlatformScopeID: "local"}
	old := &BindingSnapshot{Scope: scope, SessionID: "same", Version: 1}
	next := &BindingSnapshot{Scope: scope, SessionID: "same", Version: 2}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	displayed := make(chan string, 4)
	d.send = func(ctx context.Context, _ messageData, snapshot runtimestatus.Snapshot) error {
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
	conversation := &contextinfo.Conversation{PlatformData: messageData{adapter: New()}}
	event := StatusChangedEvent{Conversation: conversation, Binding: old, Version: 100, Snapshot: runtimestatus.Snapshot{SessionID: "same", Provider: "old", Phase: runtimestatus.PhaseLLM}}
	if err := d.receive(event); err != nil {
		t.Fatal(err)
	}
	<-entered
	event.Version, event.Snapshot.Provider = 101, "old pending"
	if err := d.receive(event); err != nil {
		t.Fatal(err)
	}
	if err := d.forget(BindingChangedEvent{Version: 2, Old: old, New: next}); err != nil {
		t.Fatal(err)
	}
	// 状态版本和绑定版本各自独立，新激活不沿用旧状态的版本水位。
	event.Binding, event.Version, event.Snapshot.Provider = next, 1, "new"
	event.Snapshot.Phase = runtimestatus.PhaseDone
	if err := d.receive(event); err != nil {
		t.Fatal(err)
	}
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
	if err := d.forget(BindingChangedEvent{Version: 1, New: old}); err != nil {
		t.Fatal(err)
	}
	event.Binding, event.Version, event.Snapshot.Provider = old, 101, "late old"
	if err := d.receive(event); err != nil {
		t.Fatal(err)
	}
	if err := d.forget(BindingChangedEvent{Version: 3, Old: next}); err != nil {
		t.Fatal(err)
	}
	event.Binding, event.Version, event.Snapshot.Provider = next, 2, "late unbound"
	if err := d.receive(event); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(displayed) != 0 {
		t.Fatal("stale binding resurrected")
	}
}

func TestStatusTerminalWatermarkOutlivesPendingBody(t *testing.T) {
	d := newStatusDisplay()
	defer d.Close(context.Background())
	sent := make(chan struct{}, 2)
	d.send = func(context.Context, messageData, runtimestatus.Snapshot) error { sent <- struct{}{}; return nil }
	event := StatusChangedEvent{Conversation: &contextinfo.Conversation{PlatformData: messageData{adapter: New()}}, Version: 10, Snapshot: runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseDone}}
	if err := d.receive(event); err != nil {
		t.Fatal(err)
	}
	<-sent
	deadline := time.Now().Add(time.Second)
	for {
		d.mu.Lock()
		empty := len(d.latest) == 0
		d.mu.Unlock()
		if empty {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal body retained")
		}
		time.Sleep(time.Millisecond)
	}
	event.Version, event.Snapshot.Phase = 9, runtimestatus.PhaseLLM
	if err := d.receive(event); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	pending := len(d.latest)
	d.mu.Unlock()
	if pending != 0 {
		t.Fatal("old state recreated terminal projection")
	}
}
