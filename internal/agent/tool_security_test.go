package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/memory/resident"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/tool/builtin"
	"elbot/internal/turn"
)

func TestRiskConfirmationStopUsesStopCommandWithoutToolError(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "shell", Args: `{"cmd":"rm out.txt"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "should not continue"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	registry := tool.NewRegistry()
	_ = registry.Register(tool.NewDiscoverTool(registry))
	_ = registry.Register(newAgentShellTool())
	a.SetToolRuntime(registry, nil)
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "危险命令") }()

	var current *storage.Session
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		session, err := a.sessions.Current(ctx, a.scope(context.Background()))
		if err == nil && a.turns.Snapshot(session.ID).Phase == turn.PhaseAwaitRiskConfirm {
			current = session
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if current == nil {
		t.Fatal("did not enter risk confirmation phase")
	}

	if err := a.HandleMessage(ctx, "/stop"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("chat returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("chat did not stop")
	}
	if got := a.turns.Snapshot(current.ID).Phase; got != turn.PhaseIdle {
		t.Fatalf("turn phase = %s", got)
	}
	if requests := f.chatRequests(); len(requests) != 1 {
		t.Fatalf("chat requests = %d, want 1", len(requests))
	}
	out := p.out.String() + p.preview.String()
	if strings.Contains(out, "should not continue") || strings.Contains(out, "stopped while waiting") || strings.Contains(out, "assistant: error") {
		t.Fatalf("unexpected stop output: %q", out)
	}
	if !strings.Contains(p.out.String(), "stopped") {
		t.Fatalf("missing stop command output: %q", p.out.String())
	}
}

func TestRiskConfirmationDetailUsesToolProvidedDetail(t *testing.T) {
	p := &fakePlatform{}
	a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx := context.Background()
	session, err := a.sessions.Create(ctx, a.scope(context.Background()), session.CreateRequest{Title: "confirm custom detail"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if !a.turns.StartLLM(session.ID, "run tool") || !a.turns.StartToolPhase(session.ID) {
		t.Fatal("failed to enter tool phase")
	}
	done := make(chan turn.RiskConfirmationResponse, 1)
	go func() {
		resp, _ := a.turns.AwaitRiskConfirmation(session.ID, turn.RiskConfirmation{ID: "call_1", ToolName: "edit_file", Arguments: `{"path":"a.txt"}`, Risk: "high", Detail: "文件：a.txt\n编辑 1/1：替换行\n新内容：\n  line 1\n  line 2"})
		done <- resp
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		time.Sleep(10 * time.Millisecond)
	}
	if a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
		t.Fatal("did not enter risk confirmation phase")
	}
	if err := a.HandleMessage(ctx, "/detail"); err != nil {
		t.Fatalf("detail: %v", err)
	}
	out := p.out.String()
	for _, want := range []string{"文件：a.txt", "编辑 1/1：替换行", "  line 1\n", "  line 2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "path: a.txt") {
		t.Fatalf("detail should prefer tool detail over fallback: %q", out)
	}
	select {
	case resp := <-done:
		t.Fatalf("detail should not resolve confirmation: %#v", resp)
	default:
	}
}

func TestRiskConfirmationConfirmToolAndConfirmAllAliases(t *testing.T) {
	t.Run("confirm tool alias", func(t *testing.T) {
		p := &fakePlatform{}
		a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
		ctx := context.Background()
		session, err := a.sessions.Create(ctx, a.scope(context.Background()), session.CreateRequest{Title: "confirm tool"})
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		if !a.turns.StartLLM(session.ID, "run tool") || !a.turns.StartToolPhase(session.ID) {
			t.Fatal("failed to enter tool phase")
		}
		done := make(chan turn.RiskConfirmationResponse, 1)
		go func() {
			resp, _ := a.turns.AwaitRiskConfirmation(session.ID, turn.RiskConfirmation{ID: "call_1", ToolName: "shell", Arguments: `{"cmd":"rm x"}`, Risk: "high"})
			done <- resp
		}()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) && a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
			time.Sleep(10 * time.Millisecond)
		}
		if err := a.HandleMessage(ctx, "/ct"); err != nil {
			t.Fatalf("confirm tool alias: %v", err)
		}
		select {
		case resp := <-done:
			if !resp.Confirmed || !resp.ConfirmTool {
				t.Fatalf("response = %#v", resp)
			}
		case <-time.After(time.Second):
			t.Fatal("confirm tool did not resolve")
		}
	})

	t.Run("confirm all alias", func(t *testing.T) {
		p := &fakePlatform{}
		a := New(p, &fakeLLM{}, "test-model", config.ProviderConfig{}, newTestStore(t))
		ctx := context.Background()
		session, err := a.sessions.Create(ctx, a.scope(context.Background()), session.CreateRequest{Title: "confirm all"})
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		if !a.turns.StartLLM(session.ID, "run tool") || !a.turns.StartToolPhase(session.ID) {
			t.Fatal("failed to enter tool phase")
		}
		done := make(chan turn.RiskConfirmationResponse, 1)
		go func() {
			resp, _ := a.turns.AwaitRiskConfirmation(session.ID, turn.RiskConfirmation{ID: "call_2", ToolName: "cron", Arguments: `{}`, Risk: "high"})
			done <- resp
		}()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) && a.turns.Snapshot(session.ID).Phase != turn.PhaseAwaitRiskConfirm {
			time.Sleep(10 * time.Millisecond)
		}
		if err := a.HandleMessage(ctx, "/ca"); err != nil {
			t.Fatalf("confirm all alias: %v", err)
		}
		select {
		case resp := <-done:
			if !resp.Confirmed || !resp.ConfirmAll {
				t.Fatalf("response = %#v", resp)
			}
		case <-time.After(time.Second):
			t.Fatal("confirm all did not resolve")
		}
	})
}

func TestRegularUserMustConfirmHighRiskOwnerScopedTool(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "resident_memory_core", Args: `{"content":"我喜欢咖啡"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "已记下"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	if err := registry.Register(tool.NewDiscoverTool(registry)); err != nil {
		t.Fatal(err)
	}
	memStore := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	for _, tl := range builtin.NewResidentMemoryTools(memStore) {
		if err := registry.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	a.SetToolRuntime(registry, nil)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "cli", PlatformUserID: "regular", ScopeID: "shared"})

	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "更新我的核心记忆为：我喜欢咖啡") }()

	var current *storage.Session
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sessionRow, err := a.sessions.Current(ctx, a.scope(ctx))
		if err == nil && a.turns.Snapshot(sessionRow.ID).Phase == turn.PhaseAwaitRiskConfirm {
			current = sessionRow
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if current == nil {
		t.Fatal("regular user did not enter risk confirmation phase")
	}

	scope := resident.ActorScope(security.Actor{ID: "cli:regular", Platform: "cli", PlatformUserID: "regular", Role: security.RoleUser})
	if _, err := memStore.Read(ctx, scope); !errors.Is(err, resident.ErrNotFound) {
		t.Fatalf("core memory changed before confirmation: %v", err)
	}

	otherCtx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "cli", PlatformUserID: "other", ScopeID: "shared"})
	if err := a.HandleMessage(otherCtx, "/confirm"); err != nil {
		t.Fatalf("other user confirm: %v", err)
	}
	if got := a.turns.Snapshot(current.ID).Phase; got != turn.PhaseAwaitRiskConfirm {
		t.Fatalf("other user changed confirmation phase to %s", got)
	}

	if err := a.HandleMessage(ctx, "/confirm"); err != nil {
		t.Fatalf("owner confirm: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleMessage: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("chat did not continue after confirmation")
	}
	if !strings.Contains(p.out.String(), "已记下") {
		t.Fatalf("owner-scoped tool should succeed for regular user, output = %q", p.out.String())
	}
	mem, err := memStore.Read(ctx, scope)
	if err != nil {
		t.Fatalf("read memory: %v", err)
	}
	if mem.Core != "我喜欢咖啡" {
		t.Fatalf("core memory = %q, want 我喜欢咖啡", mem.Core)
	}
}

func TestRegularUserCanUpdateNormalMemoryWithoutConfirmation(t *testing.T) {
	p := &fakePlatform{}
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "resident_memory_normal", Args: `{"action":"write","content":"用户喜欢短回复。"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "已记下"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, newTestStore(t))
	a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	if err := registry.Register(tool.NewDiscoverTool(registry)); err != nil {
		t.Fatal(err)
	}
	memStore := resident.NewStore(filepath.Join(t.TempDir(), "memories.toml"))
	for _, tl := range builtin.NewResidentMemoryTools(memStore) {
		if err := registry.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	a.SetToolRuntime(registry, nil)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "cli", PlatformUserID: "regular", ScopeID: "regular"})

	if err := a.HandleMessage(ctx, "更新我的普通记忆"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if strings.Contains(p.out.String(), "等待确认") {
		t.Fatalf("normal memory should not require confirmation, output = %q", p.out.String())
	}
	scope := resident.ActorScope(security.Actor{ID: "cli:regular", Platform: "cli", PlatformUserID: "regular", Role: security.RoleUser})
	mem, err := memStore.Read(ctx, scope)
	if err != nil {
		t.Fatalf("read memory: %v", err)
	}
	if mem.Normal != "用户喜欢短回复。" {
		t.Fatalf("normal memory = %q", mem.Normal)
	}
}

func TestRegularUserCannotCallSuperadminOnlyTool(t *testing.T) {
	p := &fakePlatform{}
	store := newTestStore(t)
	f := &fakeLLM{chunks: [][]llm.StreamChunk{
		{{ToolCallDeltas: []llm.ToolCallDelta{{ID: "call_1", Name: "long_memory_write", Args: `{"category":"x","title":"t","summary":"s","content":"c"}`}}, FinishReason: "tool_calls"}},
		{{DeltaContent: "fallback"}},
	}}
	a := New(p, f, "test-model", config.ProviderConfig{}, store)
	a.SetSecurityPolicy(security.NewPolicy("low", "high", map[string][]string{"cli": {"local"}}))
	registry := tool.NewRegistry()
	if err := registry.Register(tool.NewDiscoverTool(registry)); err != nil {
		t.Fatal(err)
	}
	for _, tl := range builtin.NewLongMemoryTools(t.TempDir()) {
		if err := registry.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	a.SetToolRuntime(registry, nil)
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Platform: "cli", PlatformUserID: "regular", ScopeID: "regular"})

	if err := a.HandleMessage(ctx, "写长期记忆"); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	requests := f.chatRequests()
	if len(requests) < 2 {
		t.Fatalf("expected followup LLM request with tool result, got %d requests", len(requests))
	}
	followup := requests[1]
	var toolMsg string
	for _, m := range followup.Messages {
		if m.Role == llm.RoleTool && m.Name == "long_memory_write" {
			toolMsg = llm.SegmentsContentText(m.Segments)
		}
	}
	if toolMsg == "" {
		t.Fatalf("missing tool result message in followup: %#v", followup.Messages)
	}
	if !strings.Contains(toolMsg, "superadmin") {
		t.Fatalf("superadmin-only tool should be denied for regular user, tool message = %q", toolMsg)
	}
}

func TestAuthorizedActorsConfirmHighRiskTools(t *testing.T) {
	policy := security.DefaultPolicy()
	if !policy.NeedsToolConfirmation(security.Actor{Role: security.RoleSuperadmin}, security.RiskHigh) {
		t.Fatalf("superadmin should need confirmation for high risk")
	}
	if !policy.NeedsToolConfirmation(security.Actor{Role: security.RoleUser}, security.RiskHigh) {
		t.Fatalf("regular user should confirm high-risk authorized tools")
	}
	if policy.NeedsToolConfirmation(security.Actor{Role: security.RoleUser}, security.RiskMedium) {
		t.Fatalf("regular user should not confirm medium-risk tools")
	}
	if !policy.CanUseTool(security.Actor{Role: security.RoleUser}, security.RiskHigh, true) {
		t.Fatalf("regular user should be allowed for owner-scoped high risk")
	}
	if policy.CanUseTool(security.Actor{Role: security.RoleUser}, security.RiskHigh, false) {
		t.Fatalf("regular user should be denied for non-owner high risk")
	}
}
