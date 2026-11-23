package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/testutil"
	"elbot/internal/turn"
)

type coagentTestPlatform struct {
	PlatformCapabilities
	text chan string
}

type coagentNamingModels struct{ ModelCapabilities }
type coagentNamingClient struct{ isolatedClient }

func (coagentNamingClient) GenerateText(context.Context, llm.TextRequest) (llm.TextResult, error) {
	return llm.TextResult{Text: `["监工","修复者"]`}, nil
}

func (m coagentNamingModels) ResolveMode(mode string) modelmgr.Selection {
	selected := m.ModelCapabilities.ResolveMode(mode)
	selected.Client = coagentNamingClient{}
	return selected
}

func TestCoagentUserCommands(t *testing.T) {
	deps := isolatedModuleDeps(t)
	deps.Models = coagentNamingModels{deps.Models}
	m := contractSupervisor(t, deps)
	ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 5*time.Second)
	defer cancel()
	ctx = contextinfo.WithActor(ctx, deps.Permission.Actor("cli", "local", ""))
	command := func(text string) {
		t.Helper()
		if err := m.HandleMessage(ctx, MessageRequest{Text: text}); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
	}
	read := func() storage.CoagentBoard {
		t.Helper()
		r, err := deps.Control.Coagent(ctx, session.CoagentAction{Operation: "board"}, true)
		if err != nil || r.Status != operationresult.Success {
			t.Fatalf("board: %+v %v", r, err)
		}
		return r.Data
	}
	command("/co create 2 并行修复")
	b := read()
	if len(b.Members) != 3 || b.Members[1].Name != "监工" || b.Members[2].Name != "修复者" || b.PinnedText != "" {
		t.Fatal(b)
	}
	command("/co create 7 超出剩余名额")
	command("/co create 9 超出总人数")
	command("/co pin 第一行\n第二行")
	command("/co pin --goal 验收修复")
	if b := read(); b.PinnedText != "第一行\n第二行" || b.Goal != "验收修复" {
		t.Fatal(b)
	}
	command("/co pin")
	command("/co post --to " + b.Members[2].SessionID + " 请验收")
	command("/co board")
	b = read()
	if len(b.Entries) != 5 || b.PinnedText != "" || b.Entries[4].TargetSessionID != b.Members[2].SessionID || b.Entries[4].Author != "用户" {
		t.Fatal(b)
	}
}

type coagentBusyClient struct {
	isolatedClient
	waiting chan struct{}
	release chan struct{}
}

func (c coagentBusyClient) Stream(ctx context.Context, req chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	last := llm.SegmentsTextOnly(req.Messages[len(req.Messages)-1].Segments)
	if strings.Contains(last, "等待统一停止") {
		c.waiting <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.release:
		}
	}
	return c.isolatedClient.Stream(ctx, req)
}

func TestCoagentPendingStopAndCompactHandoff(t *testing.T) {
	deps := isolatedModuleDeps(t)
	client := coagentBusyClient{waiting: make(chan struct{}, 3), release: make(chan struct{})}
	contractModels(t, &deps, client)
	m := contractSupervisor(t, deps)
	t.Cleanup(func() { close(client.release) })
	ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 10*time.Second)
	defer cancel()
	actor := deps.Permission.Actor("cli", "local", "")
	ctx = contextinfo.WithActor(ctx, actor)
	scope := session.Scope{Owner: actor.ActorIdentity, Platform: "cli", ChatType: contextinfo.ChatPrivate, ChatID: "local", IsSuperadmin: true}
	main, err := m.sessions.GetOrCreateCurrent(ctx, scope, "主会话")
	if err != nil || main.Status != operationresult.Success {
		t.Fatal(main, err)
	}
	dir := t.TempDir()
	if err := m.sessions.WorkspaceStore(main.Data.ID).SetWorkspaceDir(ctx, dir); err != nil {
		t.Fatal(err)
	}
	act := func(a session.CoagentAction) storage.CoagentBoard {
		t.Helper()
		r, err := deps.Control.Coagent(ctx, a, false)
		if err != nil || r.Status != operationresult.Success {
			t.Fatalf("%s: %s %s %v", a.Operation, r.Status, r.Message, err)
		}
		return r.Data
	}
	board := act(session.CoagentAction{Operation: "create", Goal: "等待统一停止", Names: []string{"A", "B"}})
	for range board.Members {
		select {
		case <-client.waiting:
		case <-ctx.Done():
			t.Fatal("sessions did not start concurrently")
		}
	}
	for _, member := range board.Members {
		state, err := m.sessions.WorkspaceStore(member.SessionID).GetWorkspaceState(ctx)
		if err != nil || state.Dir != dir {
			t.Fatalf("workspace for %s: %v %v", member.Name, state, err)
		}
	}
	a := board.Members[1]
	post := func(target, text string) {
		act(session.CoagentAction{Operation: "post", TargetSessionID: target, Text: text})
	}
	post(a.SessionID, "排队后取消")
	if m.turns.Snapshot(a.SessionID).PendingCount != 1 {
		t.Fatal("busy member did not queue the board update")
	}
	if res, err := deps.Control.StopAllRequests(ctx); err != nil || res.Status != operationresult.Success {
		t.Fatal(res, err)
	}
	for _, member := range board.Members {
		for m.turns.Snapshot(member.SessionID).Phase != turn.PhaseIdle || len(m.requests.ListBySession(member.SessionID)) != 0 {
			select {
			case <-ctx.Done():
				t.Fatalf("session %s did not stop", member.Name)
			case <-time.After(time.Millisecond):
			}
		}
	}
	// Hold the compaction boundary while a real board update arrives, then commit
	// the new session using the same binding gate as the compaction coordinator.
	if !m.turns.StartCompactRun(a.SessionID, "test-compact", nil) {
		t.Fatal("compact admission failed")
	}
	post(a.SessionID, "压缩后交付")
	scope.TargetSessionID = a.SessionID
	nextID := "compacted-a"
	locked, release, err := m.sessions.EnterActivation(ctx, scope, a.SessionID, nextID)
	if err != nil {
		t.Fatal(err)
	}
	m.turns.CompleteCompactRun(a.SessionID, "test-compact")
	res, err := m.sessions.CreateCompacted(locked, scope, a.SessionID, session.CompactedRequest{ID: nextID, Title: a.Name})
	release()
	if err != nil || res.Status != operationresult.Success {
		t.Fatal(res, err)
	}
	for {
		rows, err := deps.Store.Messages().ListBySession(ctx, nextID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) >= 2 {
			if !strings.Contains(rows[0].Content, "压缩后交付") || rows[1].Content != "isolated answer" {
				t.Fatal(rows)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("message did not follow compacted member")
		case <-time.After(time.Millisecond):
		}
	}
}

func (p coagentTestPlatform) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (operationresult.Result[delivery.Receipt], error) {
	for _, o := range outputs {
		p.text <- o.Text
	}
	return p.PlatformCapabilities.SendTo(ctx, target, outputs)
}

func TestCoagentBoardDeliveryAndPrivateReplies(t *testing.T) {
	deps := isolatedModuleDeps(t)
	display := make(chan string, 32)
	deps.Platform = coagentTestPlatform{deps.Platform, display}
	m, err := NewSupervisor(t.Context(), isolatedAgentConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 5*time.Second)
	defer cancel()
	ctx = contextinfo.WithActor(ctx, deps.Permission.Actor("cli", "local", ""))
	if err := m.HandleMessage(ctx, MessageRequest{Text: "主窗口原始输入"}); err != nil {
		t.Fatal(err)
	}
	act := func(a session.CoagentAction) storage.CoagentBoard {
		t.Helper()
		r, err := deps.Control.Coagent(ctx, a, false)
		if err != nil || r.Status != operationresult.Success {
			t.Fatalf("%s: %s %s %v", a.Operation, r.Status, r.Message, err)
		}
		return r.Data
	}
	board := act(session.CoagentAction{Operation: "create", Goal: "并行验证", Names: []string{"修复者", "验收员"}})
	await := func(names ...string) {
		t.Helper()
		remaining := map[string]bool{}
		for _, name := range names {
			remaining["["+name+"]：isolated answer"] = true
		}
		for len(remaining) > 0 {
			select {
			case text := <-display:
				delete(remaining, text)
			case <-ctx.Done():
				t.Fatalf("missing replies: %v", remaining)
			}
		}
	}
	await("修复者", "验收员")
	target := board.Members[1]
	act(session.CoagentAction{Operation: "post", TargetSessionID: target.SessionID, Text: "仅定向验收"})
	await(target.Name)
	act(session.CoagentAction{Operation: "post", Text: "再次广播"})
	await("修复者", "验收员")
	for _, member := range board.Members {
		rows, err := deps.Store.Messages().ListBySession(ctx, member.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		var content strings.Builder
		for _, row := range rows {
			content.WriteString(row.Content)
		}
		history := content.String()
		if strings.Contains(history, "仅定向验收") != (member == target) ||
			strings.Contains(history, "主窗口原始输入") != (member.SessionID == board.MainSessionID) ||
			strings.Contains(history, "：isolated answer") {
			t.Fatalf("unexpected history for %s: %s", member.Name, history)
		}
	}
}
