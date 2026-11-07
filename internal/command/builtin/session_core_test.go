package builtin

import (
	"context"
	command "elbot/internal/command/share"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/testutil"
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
			scope := session.Scope{Owner: contextinfo.ActorIdentity{Platform: "cli", ID: "u1"}, Platform: "cli", ChatID: "local", IsCLI: true, ChatType: "private"}
			empty, err := NewStatus(Deps{Sessions: svc, Models: models, Scope: func(context.Context) session.Scope { return scope }}).Handle(ctx, command.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(commandResultText(empty), "session=nil") || strings.Contains(commandResultText(empty), "requests") || strings.Contains(commandResultText(empty), "tools") || strings.Contains(commandResultText(empty), "\n\n") {
				t.Fatalf("empty status = %q", commandResultText(empty))
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
			requests := request.NewManager(testutil.IDSequence())
			_, _, done, _ := requests.Start(ctx, request.StartRequest{SessionID: row.ID, Kind: request.KindTurn, Label: "chat"})
			defer done()
			handler := NewStatus(Deps{CommandText: testCommandText,
				Sessions: svc, Models: models, Requests: requests, Turns: turn.NewSupervisor(testutil.IDSequence()),
				Store: store, Scope: func(context.Context) session.Scope { return scope },
			})
			result, err := handler.Handle(ctx, command.Request{})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{row.ID, "status test", "requests:", "[1] turn: chat", "user: nil", "bot: nil"} {
				if !strings.Contains(commandResultText(result), want) {
					t.Fatalf("status missing %q:\n%s", want, commandResultText(result))
				}
			}
			if strings.Contains(commandResultText(result), "      session:") || strings.Count(commandResultText(result), "status test") != 1 {
				t.Fatalf("status repeats session information:\n%s", commandResultText(result))
			}
			want := "msgs=0\n" + tc.want + "created="
			if !strings.Contains(commandResultText(result), want) {
				t.Fatalf("status = %q, want contiguous block %q", commandResultText(result), want)
			}
			if strings.Contains(commandResultText(result), "pinned:") || strings.Contains(commandResultText(result), "archived:") || strings.Contains(commandResultText(result), "\n\n") {
				t.Fatalf("status contains old fields or blank lines: %q", commandResultText(result))
			}
			done()
			result, err = handler.Handle(ctx, command.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(commandResultText(result), "requests") || strings.Contains(commandResultText(result), "tools.") || strings.Contains(commandResultText(result), "\n\n") {
				t.Fatalf("idle status contains empty sections: %q", commandResultText(result))
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
			saved := map[string]int{}
			if tc.saved {
				saved["shell"] = 1
			}
			if got := formatToolUsage(saved, tc.live); got != tc.want {
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
	item := command.SessionSummaryData{SessionData: command.SessionData{ID: "s1", Title: "one", UpdatedAt: time.Date(2026, 1, 1, 1, 2, 3, 0, time.UTC)}, MessageCount: 2, Messages: []command.MessageData{{Role: "user", Content: "question"}, {Role: "assistant", Content: "answer"}}}
	content := commandResultText(&command.Result{Kind: command.ResultKindSessions, Data: command.SessionsData{Page: command.Page{Page: 1, PageSize: 10, HasNext: true}, Items: []command.SessionSummaryData{item}, NextCommand: "/sessions 2 foo"}})
	if !strings.Contains(content, "page=1") || !strings.Contains(content, "next: /sessions 2 foo") || !strings.Contains(content, "user: question / bot: answer") {
		t.Fatalf("content = %q", content)
	}
	resume := commandResultText(&command.Result{Kind: command.ResultKindSessions, Data: command.SessionsData{Page: command.Page{Page: 2, PageSize: 10}, Items: []command.SessionSummaryData{item}, PrevCommand: "/resume --page 1"}})
	if !strings.Contains(resume, "prev: /resume --page 1") || !strings.Contains(resume, "user: question / bot: answer") {
		t.Fatalf("resume content = %q", resume)
	}
	resumableItem := command.SessionSummaryData{SessionData: command.SessionData{ID: "s11", Title: "eleven"}, Messages: item.Messages}
	resumable := commandResultText(&command.Result{Kind: command.ResultKindSessions, Data: command.SessionsData{Page: command.Page{Page: 2, PageSize: 10}, Items: []command.SessionSummaryData{resumableItem}, PrevCommand: "/resume --page 1"}})
	if !strings.Contains(resumable, "[11] eleven") || !strings.Contains(resumable, "prev: /resume --page 1") || !strings.Contains(resumable, "user: question / bot: answer") {
		t.Fatalf("resumable content = %q", resumable)
	}
}
