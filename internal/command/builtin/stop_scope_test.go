package builtin

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/command"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/turn"
)

func TestStopUserScopeResolutionAndChildren(t *testing.T) {
	for _, target := range []string{"number", "id", "tool", "hook", "all"} {
		t.Run(target, func(t *testing.T) {
			ctx := security.WithActor(context.Background(), security.Actor{Role: security.RoleUser})
			sessions := session.NewService(newCommandTestStore(t))
			scope := session.Scope{ActorID: "qq:one", Platform: "qq", PlatformScopeID: "one"}
			row, err := sessions.Create(ctx, scope, session.CreateRequest{Title: "current"})
			if err != nil {
				t.Fatal(err)
			}
			requests := request.NewManager(0)
			start := func(sessionID, parentID string, kind request.Kind) (request.Request, context.Context) {
				r, run, done, err := requests.Start(ctx, request.StartRequest{SessionID: sessionID, ParentID: parentID, Kind: kind})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(done)
				return r, run
			}
			foreign, foreignCtx := start("foreign", "", request.KindTurn)
			root, rootCtx := start(row.ID, "", request.KindTurn)
			_, toolCtx := start(row.ID, root.ID, request.KindTool)
			hookReq, hookCtx := start(row.ID, root.ID, request.KindHook)
			cmd := NewStop(Deps{Sessions: sessions, Requests: requests, Turns: turn.NewManager(), Scope: func(context.Context) session.Scope { return scope }})
			completer := cmd.(command.Completer)
			completions := completer.Complete(ctx, command.CompletionRequest{Raw: "/stop ", Prefix: "/", Name: "stop", Cursor: 6})
			if len(completions) != 3 {
				t.Fatalf("completion=%+v", completions)
			}
			for _, c := range completions {
				if c.Text == foreign.ID {
					t.Fatal("foreign request disclosed")
				}
			}
			if _, err := cmd.Handle(ctx, command.Request{Args: foreign.ID}); err != nil {
				t.Fatal(err)
			}
			if foreignCtx.Err() != nil {
				t.Fatal("foreign ID canceled")
			}
			arg := map[string]string{"number": "1", "id": root.ID, "tool": "1.1", "hook": hookReq.ID, "all": ""}[target]
			if _, err := cmd.Handle(ctx, command.Request{Args: arg}); err != nil {
				t.Fatal(err)
			}
			if foreignCtx.Err() != nil {
				t.Fatal("foreign request canceled")
			}
			if target == "tool" || target == "hook" {
				if rootCtx.Err() != nil {
					t.Fatal("child stop canceled parent")
				}
				if target == "tool" {
					assertCommandTestCanceled(t, toolCtx)
					if hookCtx.Err() != nil {
						t.Fatal("sibling hook canceled")
					}
				}
				if target == "hook" {
					assertCommandTestCanceled(t, hookCtx)
					if toolCtx.Err() != nil {
						t.Fatal("sibling tool canceled")
					}
				}
			} else {
				assertCommandTestCanceled(t, rootCtx)
				assertCommandTestCanceled(t, toolCtx)
				assertCommandTestCanceled(t, hookCtx)
			}
		})
	}
}

func TestStopRejectsMissingOrExpiredCurrent(t *testing.T) {
	for _, state := range []string{"no current", "other current", "resumed current"} {
		t.Run(state, func(t *testing.T) {
			ctx := security.WithActor(context.Background(), security.Actor{Role: security.RoleUser})
			svc := session.NewService(newCommandTestStore(t))
			scope := session.Scope{ActorID: "one", Platform: "qq", PlatformScopeID: "one"}
			source, err := svc.Create(ctx, scope, session.CreateRequest{})
			if err != nil {
				t.Fatal(err)
			}
			_, binding, err := svc.CurrentBound(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			requests := request.NewManager(0)
			r, running, done, err := requests.Start(ctx, request.StartRequest{SessionID: source.ID, Kind: request.KindTurn})
			if err != nil {
				t.Fatal(err)
			}
			defer done()
			if err := svc.ResetCurrent(ctx, scope); err != nil {
				t.Fatal(err)
			}
			if state == "other current" {
				if _, err := svc.Create(ctx, scope, session.CreateRequest{}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "resumed current" {
				if _, err := svc.Resume(ctx, scope, source.ID); err != nil {
					t.Fatal(err)
				}
				ctx = session.WithBinding(ctx, binding)
			}
			cmd := NewStop(Deps{Sessions: svc, Requests: requests, Turns: turn.NewManager(), Scope: func(context.Context) session.Scope { return scope }})
			for _, arg := range []string{r.ID, "1", ""} {
				result, err := cmd.Handle(ctx, command.Request{Args: arg})
				if err != nil {
					t.Fatal(err)
				}
				if running.Err() != nil || strings.HasPrefix(result.Content, "stopped 1") {
					t.Fatalf("stale cancellation: %s", result.Content)
				}
			}
			if got := cmd.(command.Completer).Complete(ctx, command.CompletionRequest{Raw: "/stop ", Prefix: "/", Name: "stop", Cursor: 6}); len(got) != 0 {
				t.Fatalf("completion=%+v", got)
			}
		})
	}
}
