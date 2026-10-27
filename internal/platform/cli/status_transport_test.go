package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"github.com/coder/websocket/wsjson"
)

func TestStatusInFlightWriteKeepsRemoteConnection(t *testing.T) {
	for _, closing := range []bool{false, true} {
		name := "binding change"
		if closing {
			name = "display close"
		}
		t.Run(name, func(t *testing.T) {
			s := testRemoteServer(t)
			if err := s.Start(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			conn := dialTestRemote(t, s)
			conn.SetReadLimit(32 << 20)
			clients, err := s.onlineClients(func(string) bool { return true })
			if err != nil {
				t.Fatal(err)
			}
			d := newStatusDisplay()
			defer d.Close(context.Background())
			// 出错时先释放连接，避免清理仍等待背压中的写入。
			defer clients[0].conn.CloseNow()
			entered, finished := make(chan context.Context, 1), make(chan error, 2)
			d.send = func(ctx context.Context, target messageData, snapshot runtimestatus.Snapshot) error {
				if snapshot.SessionID == "old" {
					entered <- ctx
				}
				err := target.setRuntimeStatus(ctx, snapshot)
				finished <- err
				return err
			}
			scope := session.Scope{ActorID: "cli:user", Platform: "cli", PlatformScopeID: "user"}
			old := &BindingSnapshot{Scope: scope, SessionID: "old", Version: 1}
			next := &BindingSnapshot{Scope: scope, SessionID: "new", Version: 2}
			// 大载荷加暂停读取制造真实 TCP 背压，确保取消发生在写入期间。
			event := StatusChangedEvent{Binding: old, Version: 1,
				Conversation: &contextinfo.Conversation{PlatformData: messageData{client: clients[0]}},
				Snapshot:     runtimestatus.Snapshot{SessionID: "old", Provider: strings.Repeat("x", 16<<20)},
			}
			if err := d.receive(event); err != nil {
				t.Fatal(err)
			}
			sending := <-entered
			select {
			case err := <-finished:
				t.Fatalf("write did not remain in flight: %v", err)
			case <-time.After(150 * time.Millisecond):
			}
			if closing {
				d.BeginClose()
			} else {
				if err := d.forget(BindingChangedEvent{Version: 2, Old: old, New: next}); err != nil {
					t.Fatal(err)
				}
				event.Binding, event.Snapshot = next, runtimestatus.Snapshot{SessionID: "new", Phase: runtimestatus.PhaseDone}
				if err := d.receive(event); err != nil {
					t.Fatal(err)
				}
			}
			if sending.Err() != nil {
				t.Fatal("in-flight transport was canceled", sending.Err())
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			want := []string{"old", "new"}
			if closing {
				want = want[:1]
			}
			for _, sessionID := range want {
				var message remoteMessage
				if err := wsjson.Read(ctx, conn, &message); err != nil {
					t.Fatal(err)
				}
				if message.Snapshot.SessionID != sessionID {
					t.Fatalf("session = %q, want %q", message.Snapshot.SessionID, sessionID)
				}
				if err := <-finished; err != nil {
					t.Fatal(err)
				}
			}
			if err := d.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.SendTo(ctx, delivery.Target{Platform: "cli", PrivateUserID: "user"}, []delivery.Output{delivery.Text("still connected")}); err != nil {
				t.Fatal(err)
			}
			var message remoteMessage
			if err := wsjson.Read(ctx, conn, &message); err != nil || message.Text != "still connected" {
				t.Fatalf("connection not preserved: %v", err)
			}
		})
	}
}
