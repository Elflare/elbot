package agent

import (
	"context"
	"elbot/internal/request"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/config"
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

func TestCoagentResumePreservesMainBinding(t *testing.T) {
	deps := isolatedModuleDeps(t)
	m := contractSupervisor(t, deps)
	ctx := testutil.WithCLIConversation(t.Context())
	actor := deps.Permission.Actor("cli", "local", "")
	ctx = contextinfo.WithActor(ctx, actor)
	scope := session.Scope{Owner: actor.ActorIdentity, Platform: "cli", ChatType: contextinfo.ChatPrivate, ChatID: "local", IsSuperadmin: true}
	main, err := m.sessions.GetOrCreateCurrent(ctx, scope, "主窗口")
	if err != nil || main.Status != operationresult.Success {
		t.Fatal(main, err)
	}
	currentBinding := func() *session.Binding {
		current, err := m.sessions.CurrentBound(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		return current.Data.Binding
	}
	binding := currentBinding()
	if _, err := m.sessions.CreateCoagents(ctx, binding, "检查", []string{"成员"}, false); err != nil {
		t.Fatal(err)
	}
	board, err := m.sessions.ReadCoagentBoard(ctx, binding, 0)
	if err != nil || board.Status != operationresult.Success {
		t.Fatal(board, err)
	}
	member := board.Data.Members[1].SessionID
	r, err := m.sessions.Resume(ctx, scope, member)
	if err != nil || r.Status != operationresult.Rejected || currentBinding() != binding {
		t.Fatalf("resume: %+v, error %v, main still bound %t", r, err, currentBinding() == binding)
	}
	if err := m.HandleMessage(ctx, MessageRequest{Text: "引用成员回复", ResumeSessionID: member}); err != nil {
		t.Fatal(err)
	}
	if currentBinding() != binding {
		t.Fatal("referenced reply displaced main binding")
	}
	scope.Platform = "telegram"
	r, err = m.sessions.Resume(ctx, scope, member)
	if err != nil || r.Status != operationresult.Rejected || currentBinding() != nil {
		t.Fatalf("cross-platform resume: %+v %v", r, err)
	}
}

// The first model call of the child uses a tool and reports usage over the configured
// threshold. Its next call would be in the same turn, not a later board wake-up.
type coagentThresholdClient struct {
	isolatedClient
	calls atomic.Int32
}

func (c *coagentThresholdClient) Stream(ctx context.Context, req chatcompletions.Request) (<-chan chatcompletions.Chunk, error) {
	member := false
	for _, message := range req.Messages {
		if message.Role == llm.RoleSystem && strings.Contains(llm.SegmentsTextOnly(message.Segments), "你是协作成员") {
			member = true
			break
		}
	}
	if !member {
		return c.isolatedClient.Stream(ctx, req)
	}
	if c.calls.Add(1) > 1 {
		return c.isolatedClient.Stream(ctx, req)
	}
	ch := make(chan chatcompletions.Chunk, 1)
	ch <- chatcompletions.Chunk{ToolCallDeltas: []chatcompletions.ToolCallDelta{{ID: "inspect", Name: "discover_tool", Args: `{"names":["coagent_board"]}`}}, Usage: &llm.Usage{TotalTokens: new(int64(2))}}
	close(ch)
	return ch, nil
}
func TestCoagentThresholdLeavesBeforeNextModelCall(t *testing.T) {
	deps := isolatedModuleDeps(t)
	client := new(coagentThresholdClient)
	// A 100-token test window with two synthetic tokens crosses 1%; only the loopback model fixture is used.
	models, err := newConfiguredTestModels(t, testModelOptions{Clients: map[string]llm.Client{"p": client}, Providers: map[string]config.ProviderConfig{"p": {Models: []string{"m"}}}, ModeModels: map[string]config.ModelSelection{"work": {Provider: "p", Model: "m"}}, DefaultMode: "work", Metadata: config.ModelMetadataConfig{DefaultContextWindow: 100}})
	if err != nil {
		t.Fatal(err)
	}
	deps.Models = models
	cfg := isolatedAgentConfig()
	cfg.Context.CompactEnabled, cfg.Context.CompactTriggerRatio = true, 0.01
	display := make(chan string, 16)
	deps.Platform = coagentTestPlatform{deps.Platform, display}
	m, err := NewSupervisor(t.Context(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	if err = m.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 5*time.Second)
	defer cancel()
	ctx = contextinfo.WithActor(ctx, deps.Permission.Actor("cli", "local", ""))
	if err = m.HandleMessage(ctx, MessageRequest{Text: "开始"}); err != nil {
		t.Fatal(err)
	}
	created, err := deps.Control.Coagent(ctx, session.CoagentAction{Operation: "create", Goal: "检查工具", Names: []string{"A"}}, false)
	if err != nil || created.Status != operationresult.Success {
		t.Fatal(created, err)
	}
	for {
		select {
		case text := <-display:
			if !strings.Contains(text, "[系统]") {
				continue
			}
		case <-ctx.Done():
			t.Fatalf("no threshold notice, calls = %d", client.calls.Load())
		}
		break
	}
	b, err := deps.Store.Coagents().ReadBoard(ctx, created.Data.MainSessionID, 0)
	if err != nil || len(b.Members) != 1 || len(b.Entries) != 2 || b.Entries[1].Author != "系统" || client.calls.Load() != 1 {
		t.Fatal(b, client.calls.Load(), err)
	}
	if _, err = deps.Store.Sessions().Get(ctx, created.Data.Members[1].SessionID); err != nil {
		t.Fatal(err)
	}
}

type coagentCommitStore struct {
	storage.Store
	repo storage.CoagentRepository
}

func (s coagentCommitStore) Coagents() storage.CoagentRepository { return s.repo }

type coagentCommitRepo struct {
	storage.CoagentRepository
	afterPost func()
}

func (r *coagentCommitRepo) Post(ctx context.Context, id string, user bool, target, text string) (storage.CoagentBoard, error) {
	b, err := r.CoagentRepository.Post(ctx, id, user, target, text)
	if err == nil {
		r.afterPost()
	}
	return b, err
}

func TestCoagentRecipientCompactsBetweenCommitAndDispatch(t *testing.T) {
	deps := isolatedModuleDeps(t)
	repo := &coagentCommitRepo{CoagentRepository: deps.Store.Coagents()}
	deps.Store = coagentCommitStore{deps.Store, repo}
	m := contractSupervisor(t, deps)
	ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 5*time.Second)
	defer cancel()
	actor := deps.Permission.Actor("cli", "local", "")
	ctx = contextinfo.WithActor(ctx, actor)
	scope := session.Scope{Owner: actor.ActorIdentity, Platform: "cli", ChatType: contextinfo.ChatPrivate, ChatID: "local", IsSuperadmin: true}
	if r, err := m.sessions.GetOrCreateCurrent(ctx, scope, "主窗口"); err != nil || r.Status != operationresult.Success {
		t.Fatal(r, err)
	}
	bound, err := m.sessions.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	created, err := m.sessions.CreateCoagents(ctx, bound.Data.Binding, "检查", []string{"A"}, false)
	if err != nil || created.Status != operationresult.Success {
		t.Fatal(created, err)
	}
	a := created.Data.Members[1]
	scope.TargetSessionID = a.SessionID
	if r, err := m.sessions.CurrentBound(ctx, scope); err != nil || r.Status != operationresult.Success {
		t.Fatal(r, err)
	}
	// Emulate a compression commit in the precise gap between Post and fan-out.
	const next = "compacted-at-commit"
	repo.afterPost = func() {
		r, err := m.sessions.CreateCompacted(ctx, scope, a.SessionID, session.CompactedRequest{ID: next, Title: a.Name})
		if err != nil || r.Status != operationresult.Success {
			t.Fatal(r, err)
		}
	}
	b, err := deps.Control.Coagent(ctx, session.CoagentAction{Operation: "post", TargetSessionID: a.SessionID, Text: "原成员专收"}, false)
	if err != nil || b.Status != operationresult.Success || b.Data.Members[1] != a || b.Data.Entries[0].TargetSessionID != a.SessionID {
		t.Fatal(b, err)
	}
	for {
		rows, err := deps.Store.Messages().ListBySession(ctx, next)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) >= 2 {
			if !strings.Contains(rows[0].Content, "原成员专收") {
				t.Fatal(rows)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("new session missed old addressed entry")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestCoagentRemoveStopsAndRejectsStaleDelivery(t *testing.T) {
	deps := isolatedModuleDeps(t)
	client := coagentBusyClient{waiting: make(chan struct{}, 1), release: make(chan struct{})}
	contractModels(t, &deps, client)
	m := contractSupervisor(t, deps)
	defer close(client.release)
	ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 5*time.Second)
	defer cancel()
	actor := deps.Permission.Actor("cli", "local", "")
	ctx = contextinfo.WithActor(ctx, actor)
	scope := session.Scope{Owner: actor.ActorIdentity, Platform: "cli", ChatType: contextinfo.ChatPrivate, ChatID: "local", IsSuperadmin: true}
	if r, err := m.sessions.GetOrCreateCurrent(ctx, scope, "主窗口"); err != nil || r.Status != operationresult.Success {
		t.Fatal(r, err)
	}
	bound, err := m.sessions.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	create := func() storage.CoagentBoard {
		t.Helper()
		r, err := m.sessions.CreateCoagents(ctx, bound.Data.Binding, "检查", []string{"A"}, false)
		if err != nil || r.Status != operationresult.Success {
			t.Fatal(r, err)
		}
		return r.Data
	}
	first := create()
	a := first.Members[1]
	b, err := deps.Control.Coagent(ctx, session.CoagentAction{Operation: "post", TargetSessionID: a.SessionID, Text: "等待统一停止"}, false)
	if err != nil || b.Status != operationresult.Success {
		t.Fatal(b, err)
	}
	select {
	case <-client.waiting:
	case <-ctx.Done():
		t.Fatal("member did not start")
	}
	if r, err := m.sessions.CreateCoagents(ctx, bound.Data.Binding, "冲突", []string{"A"}, false); err != nil || r.Status != operationresult.Conflict {
		t.Fatalf("duplicate: %+v %v", r, err)
	}
	removed, err := deps.Control.Coagent(ctx, session.CoagentAction{Operation: "remove", TargetSessionID: a.SessionID}, true)
	if err != nil || removed.Status != operationresult.Success || len(removed.Data.Members) != 1 {
		t.Fatal(removed, err)
	}
	for m.turns.Snapshot(a.SessionID).Phase != turn.PhaseIdle || len(m.requests.ListBySession(a.SessionID)) != 0 {
		select {
		case <-ctx.Done():
			t.Fatal("removed worker was not stopped")
		case <-time.After(time.Millisecond):
		}
	}
	replacement := create().Members[1]
	if replacement.SessionID == a.SessionID {
		t.Fatal(replacement)
	}
	if r, err := deps.Control.Coagent(ctx, session.CoagentAction{Operation: "post", TargetSessionID: a.SessionID, Text: "旧地址"}, true); err != nil || r.Status != operationresult.InvalidArguments {
		t.Fatal(r, err)
	}
	// A delayed fan-out for the retired recipient must neither wake it nor reroute by name.
	info, _ := contextinfo.ConversationFromContext(ctx)
	info.TargetSessionID = a.SessionID
	old := contextinfo.WithConversation(ctx, info)
	if err = m.execution.QueueBoardInput(old, first.ID, a, "旧快照", false, nil); err != nil {
		t.Fatal(err)
	}
	// The old CLI view remains explicitly addressed, but removal revoked membership.
	info.CoagentMainSessionID = first.MainSessionID
	if err := m.HandleMessage(contextinfo.WithConversation(ctx, info), MessageRequest{Text: "过期私聊不可续跑"}); err != nil {
		t.Fatal(err)
	}
	rows, err := deps.Store.Messages().ListBySession(ctx, a.SessionID)
	if err != nil || slices.ContainsFunc(rows, func(row storage.Message) bool { return row.Content == "过期私聊不可续跑" }) {
		t.Fatal(rows, err)
	}
	for _, id := range []string{a.SessionID, replacement.SessionID} {
		if m.turns.Snapshot(id).Phase != turn.PhaseIdle {
			t.Fatalf("stale delivery woke %s", id)
		}
	}
}

type coagentViewEvent struct {
	info  contextinfo.Conversation
	facts contextinfo.Execution
	text  string
}
type coagentViewPlatform struct {
	PlatformCapabilities
	events chan coagentViewEvent
}

func (p coagentViewPlatform) record(ctx context.Context, text string) {
	info, _ := contextinfo.ConversationFromContext(ctx)
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	p.events <- coagentViewEvent{info, facts, text}
}
func (p coagentViewPlatform) SendTo(ctx context.Context, target delivery.Target, outputs []delivery.Output) (operationresult.Result[delivery.Receipt], error) {
	for _, o := range outputs {
		if o.Kind == delivery.KindText {
			p.record(ctx, o.Text)
		}
	}
	return p.PlatformCapabilities.SendTo(ctx, target, outputs)
}
func (p coagentViewPlatform) StartStream(ctx context.Context) (operationresult.Result[delivery.MessageStream], error) {
	r, err := p.PlatformCapabilities.StartStream(ctx)
	if err != nil || r.Status != operationresult.Success {
		return r, err
	}
	s := r.Data
	r.Data = delivery.StreamFunc(func(write context.Context, e delivery.StreamEvent) (delivery.Receipt, error) {
		if e.Type == delivery.StreamSend {
			p.record(ctx, e.Text)
		}
		return s.Send(write, e)
	})
	return r, nil
}

func TestCoagentSelectedPrivateInputDoesNotEnterPublicRun(t *testing.T) {
	for _, stop := range []bool{false, true} {
		name := "reply"
		if stop {
			name = "stop"
		}
		t.Run(name, func(t *testing.T) {
			deps := isolatedModuleDeps(t)
			client := coagentBusyClient{waiting: make(chan struct{}, 1), release: make(chan struct{})}
			contractModels(t, &deps, client)
			events := make(chan coagentViewEvent, 64)
			deps.Platform = coagentViewPlatform{deps.Platform, events}
			m, err := NewSupervisor(t.Context(), isolatedAgentConfig(), deps)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close(context.Background()) })
			if err = m.Start(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(testutil.WithCLIConversation(t.Context()), 5*time.Second)
			defer cancel()
			actor := deps.Permission.Actor("cli", "local", "")
			ctx = contextinfo.WithActor(ctx, actor)
			scope := session.Scope{Owner: actor.ActorIdentity, Platform: "cli", ChatType: contextinfo.ChatPrivate, ChatID: "local", IsSuperadmin: true}
			if r, err := m.sessions.GetOrCreateCurrent(ctx, scope, "主"); err != nil || r.Status != operationresult.Success {
				t.Fatal(r, err)
			}
			main, err := m.sessions.CurrentBound(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			b, err := m.sessions.CreateCoagents(ctx, main.Data.Binding, "协作", []string{"A"}, false)
			if err != nil || b.Status != operationresult.Success {
				t.Fatal(b, err)
			}
			mid, id := b.Data.MainSessionID, b.Data.Members[1].SessionID
			if r, err := deps.Control.Coagent(ctx, session.CoagentAction{Operation: "post", TargetSessionID: id, Text: "等待统一停止"}, true); err != nil || r.Status != operationresult.Success {
				t.Fatal(r, err)
			}
			select {
			case <-client.waiting:
			case <-ctx.Done():
				t.Fatal("public run did not start")
			}
			info, _ := contextinfo.ConversationFromContext(ctx)
			info.TargetSessionID, info.CoagentMainSessionID = id, mid
			private := contextinfo.WithConversation(ctx, info)
			done := make(chan error, 1)
			go func() {
				done <- m.HandleMessage(private, MessageRequest{Text: "只进成员历史", ResumeSessionID: mid})
			}()
			for {
				if slices.ContainsFunc(m.requests.ListBySession(id), func(r request.Request) bool { return r.Label == "coagent_private_input" }) {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("private input returned before public turn: %v", err)
				case <-ctx.Done():
					t.Fatal("private admission not waiting")
				case <-time.After(time.Millisecond):
				}
			}
			if pending := m.turns.Snapshot(id).PendingCount; pending != 0 {
				t.Fatal("private input entered public turn", pending)
			}
			if stop {
				if r, err := deps.Control.StopSession(private); err != nil || r.Status != operationresult.Success {
					t.Fatal(r, err)
				}
			} else {
				close(client.release)
			}
			select {
			case err := <-done:
				if err != nil && (!stop || !errors.Is(err, context.Canceled)) {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("private run did not finish")
			}
			rows, err := deps.Store.Messages().ListBySession(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			got := slices.ContainsFunc(rows, func(m storage.Message) bool { return m.Content == "只进成员历史" })
			if got == stop {
				t.Fatalf("private member history on stop=%v: %+v", stop, rows)
			}
			if stop {
				return
			}
			// Quoting both the latest and an older assistant reply neither resumes nor forks a member.
			for _, r := range rows {
				if r.Role == storage.RoleAssistant {
					resume, fork, err := m.sessions.ResolveReference(ctx, scope, r)
					if err != nil || resume != "" || fork != "" {
						t.Fatal(resume, fork, err)
					}
				}
			}
			priv, pub := false, false
			for !priv || !pub {
				select {
				case e := <-events:
					if e.text == "[A]：isolated answer" && e.facts.SessionID == mid {
						pub = true
					}
					if e.text == "isolated answer" && e.facts.SessionID == id && e.info.TargetSessionID == id && e.info.CoagentMainSessionID == mid && e.facts.TaskType == "coagent" {
						priv = true
					}
				case <-ctx.Done():
					t.Fatalf("private/public route: %v/%v", priv, pub)
				}
			}
			board, err := deps.Store.Coagents().ReadBoard(ctx, id, 0)
			if err != nil || len(board.Entries) != 2 {
				t.Fatal(board, err)
			}
			mainRows, err := deps.Store.Messages().ListBySession(ctx, mid)
			if err != nil || len(mainRows) != 0 {
				t.Fatal(mainRows, err)
			}
			current, err := m.sessions.CurrentBound(ctx, scope)
			if err != nil || current.Data.Binding != main.Data.Binding {
				t.Fatal(current, err)
			}
			// A board tool invoked from the private route still displays and wakes the main session, not the selected child.
			if r, err := deps.Control.Coagent(private, session.CoagentAction{Operation: "post", TargetSessionID: mid, Text: "给主窗口"}, false); err != nil || r.Status != operationresult.Success {
				t.Fatal(r, err)
			}
			select {
			case e := <-events:
				if !strings.Contains(e.text, "给主窗口") || e.facts.SessionID != mid || e.info.TargetSessionID != mid || e.info.CoagentMainSessionID != "" {
					t.Fatal(e)
				}
			case <-ctx.Done():
				t.Fatal("board display missing")
			}
		})
	}
}
