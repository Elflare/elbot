package builtin

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/config"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
	"strings"
	"testing"
	"time"
)

func TestStatusSessionMarkers(t *testing.T) {
	ctx := context.Background()
	models, err := modelmgr.NewModelManager(t.Context(), &config.Config{
		Providers:  map[string]config.ProviderConfig{"test": {Models: []string{"model"}}},
		ModeModels: map[string]config.ModelSelection{"work": {Provider: "test", Model: "model"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := models.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	for _, tc := range []struct {
		name             string
		pinned, archived bool
		want             string
	}{
		{name: "neither"},
		{name: "pinned", pinned: true, want: "  pinned\n"},
		{name: "archived", archived: true, want: "  archived\n"},
		{name: "both", pinned: true, archived: true, want: "  pinned, archived\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newCommandTestStore(t)
			svc := newSessionSupervisor(t, store)
			scope := session.Scope{ActorID: "u1", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
			empty, err := NewStatus(Deps{Sessions: svc, Models: models, Scope: func(context.Context) session.Scope { return scope }}).Handle(ctx, command.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(empty.Content, "session=nil") || strings.Contains(empty.Content, "requests") || strings.Contains(empty.Content, "tools") || strings.Contains(empty.Content, "\n\n") {
				t.Fatalf("empty status = %q", empty.Content)
			}
			row, err := svc.Create(ctx, scope, session.CreateRequest{Title: "status test"})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			_, err = store.Sessions().Mutate(ctx, row.ID, func(row *storage.Session) error {
				if tc.pinned {
					row.PinnedAt = &now
				}
				if tc.archived {
					row.ArchivedAt = &now
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			requests := request.NewManager(0)
			_, _, done := requests.Start(ctx, request.StartRequest{SessionID: row.ID, Kind: request.KindTurn, Label: "chat"})
			defer done()
			handler := NewStatus(Deps{CommandText: testCommandText,
				Sessions: svc, Models: models, Requests: requests, Turns: turn.NewSupervisor(),
				Store: store, Scope: func(context.Context) session.Scope { return scope },
			})
			result, err := handler.Handle(ctx, command.Request{})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{row.ID, "status test", "requests:", "[1] chat · turn", "user: nil", "bot: nil"} {
				if !strings.Contains(result.Content, want) {
					t.Fatalf("status missing %q:\n%s", want, result.Content)
				}
			}
			if strings.Contains(result.Content, "      session:") || strings.Count(result.Content, "status test") != 1 {
				t.Fatalf("status repeats session information:\n%s", result.Content)
			}
			want := "msgs=0\n" + tc.want + "created="
			if !strings.Contains(result.Content, want) {
				t.Fatalf("status = %q, want contiguous block %q", result.Content, want)
			}
			if strings.Contains(result.Content, "pinned:") || strings.Contains(result.Content, "archived:") || strings.Contains(result.Content, "\n\n") {
				t.Fatalf("status contains old fields or blank lines: %q", result.Content)
			}
			done()
			result, err = handler.Handle(ctx, command.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(result.Content, "requests") || strings.Contains(result.Content, "tools.") || strings.Contains(result.Content, "\n\n") {
				t.Fatalf("idle status contains empty sections: %q", result.Content)
			}
		})
	}
}

func TestFormatToolUsageHidesEmptyLines(t *testing.T) {
	for _, tc := range []struct {
		name  string
		saved bool
		live  map[string]int
		want  string
	}{
		{name: "empty"},
		{name: "empty map", live: map[string]int{}},
		{name: "saved only", saved: true, want: "tools.saved: shell×1\n"},
		{name: "turn only", live: map[string]int{"shell": 2}, want: "tools.turn: shell×2\n"},
		{name: "both", saved: true, live: map[string]int{"shell": 2}, want: "tools.saved: shell×1\ntools.turn: shell×2\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newCommandTestStore(t)
			row := &storage.Session{OwnerID: "u1", Platform: "cli", PlatformScopeID: "local", Title: "tools"}
			if err := store.Sessions().Create(t.Context(), row); err != nil {
				t.Fatal(err)
			}
			if tc.saved {
				if err := store.ToolCalls().Create(t.Context(), &storage.ToolCallRecord{SessionID: row.ID, ToolCallID: "call_1", ToolName: "shell", Success: true}); err != nil {
					t.Fatal(err)
				}
			}
			if got := formatToolUsage(t.Context(), Deps{Store: store}, row.ID, tc.live); got != tc.want {
				t.Fatalf("tool usage = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatTimeForDisplay(t *testing.T) {
	tm := time.Date(2026, 5, 31, 14, 24, 45, 704514100, time.FixedZone("CST", 8*60*60))
	if got := formatTime(tm); got != "2026-05-31 14:24:45" {
		t.Fatalf("formatTime = %q", got)
	}
}

func TestFormatSessionsPageShowsNextCommand(t *testing.T) {
	content := formatSessionsPage([]storage.SessionSummary{{ID: "s1", Title: "one", UpdatedAt: time.Date(2026, 1, 1, 1, 2, 3, 0, time.UTC), Messages: []storage.Message{{Role: storage.RoleUser, Content: "question"}, {Role: storage.RoleAssistant, Content: "answer"}}}}, "", 1, "foo", true, testCommandText, "sessions")
	if !strings.Contains(content, "page=1") || !strings.Contains(content, "next: /sessions 2 foo") || !strings.Contains(content, "user: question / bot: answer") {
		t.Fatalf("content = %q", content)
	}
	resume := formatSessionsPage([]storage.SessionSummary{{ID: "s1", Title: "one", UpdatedAt: time.Date(2026, 1, 1, 1, 2, 3, 0, time.UTC), Messages: []storage.Message{{Role: storage.RoleUser, Content: "question"}, {Role: storage.RoleAssistant, Content: "answer"}}}}, "", 2, "", false, testCommandText, "resume")
	if !strings.Contains(resume, "prev: /resume --page 1") || !strings.Contains(resume, "user: question / bot: answer") {
		t.Fatalf("resume content = %q", resume)
	}
	resumable := formatResumableSessionsPage([]storage.SessionSummary{{ID: "s11", Title: "eleven", Messages: []storage.Message{{Role: storage.RoleUser, Content: "question"}, {Role: storage.RoleAssistant, Content: "answer"}}}}, 2, 10, false, testCommandText)
	if !strings.Contains(resumable, "[11] eleven") || !strings.Contains(resumable, "prev: /resume --page 1") || !strings.Contains(resumable, "user: question / bot: answer") {
		t.Fatalf("resumable content = %q", resumable)
	}
}
