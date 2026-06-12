package completion

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/command"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
	"elbot/internal/turn"
)

func TestRiskConfirmationSourceCompletesOnlyDuringRiskPhase(t *testing.T) {
	ctx := context.Background()
	store := newCompletionTestStore(t)
	sessions := session.NewService(store)
	sess, err := sessions.Create(ctx, testScope(), "risk")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	turns := turn.NewManager()
	router := command.NewRouter([]string{"/"})
	source := RiskConfirmationSource{
		Router:       router,
		Sessions:     sessions,
		Turns:        turns,
		Scope:        func(context.Context) session.Scope { return testScope() },
		CommandNames: []string{"confirm", "c", "reject"},
	}
	if got := source.Complete(ctx, Request{Text: "/c"}); len(got) != 0 {
		t.Fatalf("Complete before risk phase = %#v", got)
	}
	if !turns.StartLLM(sess.ID, "run") || !turns.StartToolPhase(sess.ID) {
		t.Fatal("failed to start tool phase")
	}
	done := make(chan turn.RiskConfirmationResponse, 1)
	go func() {
		resp, _ := turns.AwaitRiskConfirmation(sess.ID, turn.RiskConfirmation{ID: "call_1", ToolName: "shell", Risk: "high"})
		done <- resp
	}()
	waitForRiskPhase(t, turns, sess.ID)

	got := source.Complete(ctx, Request{Text: "/c"})
	if len(got) != 2 || got[0].Text != "/confirm" || got[1].Text != "/c" {
		t.Fatalf("Complete during risk phase = %#v", got)
	}
	turns.ResolveRiskConfirmation(sess.ID, turn.RiskConfirmationResponse{Stopped: true})
	<-done
}

func TestForkMessageSourceCompletesAssistantMessageIDs(t *testing.T) {
	ctx := context.Background()
	store := newCompletionTestStore(t)
	sessions := session.NewService(store)
	sess, err := sessions.Create(ctx, testScope(), "fork")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	messages := []*storage.Message{
		{SessionID: sess.ID, Role: storage.RoleUser, Content: "question"},
		{ID: "abcdef-message", SessionID: sess.ID, Role: storage.RoleAssistant, Content: "answer"},
		{ID: "abc-empty", SessionID: sess.ID, Role: storage.RoleAssistant, Content: ""},
	}
	for _, message := range messages {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	source := ForkMessageSource{
		Router:   command.NewRouter([]string{"/"}),
		Sessions: sessions,
		Store:    store,
		Scope:    func(context.Context) session.Scope { return testScope() },
	}
	got := source.Complete(ctx, Request{Text: "/fork abc"})
	if len(got) != 1 || got[0].Text != "/fork abcdef-message" || got[0].Kind != KindForkMessage {
		t.Fatalf("Complete = %#v", got)
	}
}

func newCompletionTestStore(t *testing.T) storage.Store {
	t.Helper()
	store, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testScope() session.Scope {
	return session.Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
}

func waitForRiskPhase(t *testing.T, turns *turn.Manager, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if turns.Snapshot(sessionID).Phase == turn.PhaseAwaitRiskConfirm {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("did not enter risk confirmation phase")
}
