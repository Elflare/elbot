package contextmgr

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

type metadataLLM struct {
	llm.LLM
	metadata []llm.ModelMetadata
}

func (m metadataLLM) ListModelMetadata(context.Context) ([]llm.ModelMetadata, error) {
	return m.metadata, nil
}

func TestWindowResolverPriority(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"p": {ModelConfigs: map[string]config.ModelConfig{
			"manual":   {ContextWindow: 16000},
			"fallback": {ContextWindow: 12000},
		}},
	}
	resolver := NewWindowResolver(
		config.ModelMetadataConfig{DefaultContextWindow: 8192},
		providers,
		func(provider string) llm.LLM {
			return metadataLLM{metadata: []llm.ModelMetadata{{ID: "api", ContextWindow: 32000}}}
		},
	)

	if got := resolver.Resolve(context.Background(), "p", "api"); got != 32000 {
		t.Fatalf("api window = %d", got)
	}
	if got := resolver.Resolve(context.Background(), "p", "manual"); got != 16000 {
		t.Fatalf("manual provider/model window = %d", got)
	}
	if got := resolver.Resolve(context.Background(), "p", "fallback"); got != 12000 {
		t.Fatalf("fallback provider/model window = %d", got)
	}
	if got := resolver.Resolve(context.Background(), "p", "unknown"); got != 8192 {
		t.Fatalf("default window = %d", got)
	}
}

func TestLoaderUsesLatestSummary(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, t.TempDir()+"/elbot.db")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	session := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local"}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	first := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "first"}
	second := &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "second"}
	third := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "third"}
	for _, message := range []*storage.Message{first, second, third} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := store.ContextSummaries().Create(ctx, &storage.ContextSummary{SessionID: session.ID, ToMessageID: second.ID, Summary: "summary", Provider: "p", Model: "m", TriggerReason: "manual"}); err != nil {
		t.Fatalf("create summary: %v", err)
	}

	loaded, err := (Loader{Store: store}).Load(ctx, session.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Summary == nil || loaded.Summary.Summary != "summary" {
		t.Fatalf("summary = %#v", loaded.Summary)
	}
	if len(loaded.Messages) != 1 || loaded.Messages[0].ID != third.ID {
		t.Fatalf("messages = %#v", loaded.Messages)
	}
}

func TestLoaderRawMessagesIgnoresSummary(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, t.TempDir()+"/elbot.db")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	session := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local"}
	if err := store.Sessions().Create(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	first := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "first"}
	second := &storage.Message{SessionID: session.ID, Role: storage.RoleAssistant, Content: "second"}
	third := &storage.Message{SessionID: session.ID, Role: storage.RoleUser, Content: "third"}
	for _, message := range []*storage.Message{first, second, third} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := store.ContextSummaries().Create(ctx, &storage.ContextSummary{SessionID: session.ID, ToMessageID: second.ID, Summary: "summary"}); err != nil {
		t.Fatalf("create summary: %v", err)
	}

	messages, err := (Loader{Store: store}).LoadRawMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("load raw: %v", err)
	}
	if got, want := messageContents(messages), []string{"first", "second", "third"}; !equalStrings(got, want) {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
}

func TestCompactPromptAndSummaryAssembly(t *testing.T) {
	prompt := compactPrompt([]CompactMessage{
		{Role: storage.RoleUser, Content: "B"},
		{Role: storage.RoleAssistant, Content: "C", ToolCalls: []CompactToolCall{{Name: "shell", Arguments: `{"command":"go test ./..."}`}}},
		{Role: storage.RoleAssistant, Content: "H"},
	}, []string{"B", "G"})
	for _, want := range []string{"上下文内容：", "user: B", "assistant: C", `tool_call: name=shell arguments={"command":"go test ./..."}`, "用户原话：\n1. B\n2. G"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "tool result") {
		t.Fatalf("prompt contains tool result: %s", prompt)
	}

	got := assembleSummary("K\n", []string{"B", "G"})
	want := "K\n\n以下是用户原话：\n1. B\n2. G"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if got := assembleSummary("K", nil); got != "K" {
		t.Fatalf("summary without inputs = %q", got)
	}
}

func TestUsageStateAndFormatTokens(t *testing.T) {
	state := UsageState{Usage: &llm.Usage{TotalTokens: 90, CacheHitTokens: 10}, ContextWindow: 100, TriggerRatio: 0.8}
	if !state.ReachedThreshold() {
		t.Fatal("expected threshold reached")
	}
	if got := FormatTokens(state.Usage); got != "tokens：90（命中：10）" {
		t.Fatalf("tokens = %q", got)
	}
	if got := FormatTokens(nil); got != "tokens：unknown（命中：unknown）" {
		t.Fatalf("unknown tokens = %q", got)
	}
}

func TestLoaderIncludesForkParentContext(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, t.TempDir()+"/elbot.db")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	parent := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local"}
	if err := store.Sessions().Create(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	first := &storage.Message{SessionID: parent.ID, Role: storage.RoleUser, Content: "first"}
	forkPoint := &storage.Message{SessionID: parent.ID, Role: storage.RoleAssistant, Content: "fork point"}
	ignored := &storage.Message{SessionID: parent.ID, Role: storage.RoleUser, Content: "after fork"}
	for _, message := range []*storage.Message{first, forkPoint, ignored} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append parent: %v", err)
		}
	}
	fork := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local", ParentSessionID: parent.ID, ForkFromMessageID: forkPoint.ID}
	if err := store.Sessions().Create(ctx, fork); err != nil {
		t.Fatalf("create fork: %v", err)
	}
	branch := &storage.Message{SessionID: fork.ID, Role: storage.RoleUser, Content: "branch"}
	if err := store.Messages().Append(ctx, branch); err != nil {
		t.Fatalf("append branch: %v", err)
	}

	loaded, err := (Loader{Store: store}).Load(ctx, fork.ID)
	if err != nil {
		t.Fatalf("load fork: %v", err)
	}
	got := messageContents(loaded.Messages)
	want := []string{"first", "fork point", "branch"}
	if !equalStrings(got, want) {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
}

func TestLoaderUsesForkParentSummary(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, t.TempDir()+"/elbot.db")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	parent := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local"}
	if err := store.Sessions().Create(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	first := &storage.Message{SessionID: parent.ID, Role: storage.RoleUser, Content: "first"}
	second := &storage.Message{SessionID: parent.ID, Role: storage.RoleAssistant, Content: "second"}
	third := &storage.Message{SessionID: parent.ID, Role: storage.RoleAssistant, Content: "third"}
	for _, message := range []*storage.Message{first, second, third} {
		if err := store.Messages().Append(ctx, message); err != nil {
			t.Fatalf("append parent: %v", err)
		}
	}
	if err := store.ContextSummaries().Create(ctx, &storage.ContextSummary{SessionID: parent.ID, ToMessageID: second.ID, Summary: "summary", Provider: "p", Model: "m", TriggerReason: "manual"}); err != nil {
		t.Fatalf("create summary: %v", err)
	}
	fork := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local", ParentSessionID: parent.ID, ForkFromMessageID: third.ID}
	if err := store.Sessions().Create(ctx, fork); err != nil {
		t.Fatalf("create fork: %v", err)
	}

	loaded, err := (Loader{Store: store}).Load(ctx, fork.ID)
	if err != nil {
		t.Fatalf("load fork: %v", err)
	}
	if loaded.Summary == nil || loaded.Summary.Summary != "summary" {
		t.Fatalf("summary = %#v", loaded.Summary)
	}
	got := messageContents(loaded.Messages)
	want := []string{"third"}
	if !equalStrings(got, want) {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}

	raw, err := (Loader{Store: store}).LoadRawMessages(ctx, fork.ID)
	if err != nil {
		t.Fatalf("load raw fork: %v", err)
	}
	if got, want := messageContents(raw), []string{"first", "second", "third"}; !equalStrings(got, want) {
		t.Fatalf("raw messages = %#v, want %#v", got, want)
	}
}

func TestLoaderIncludesMultiLevelForkContext(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(ctx, t.TempDir()+"/elbot.db")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	root := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local"}
	if err := store.Sessions().Create(ctx, root); err != nil {
		t.Fatalf("create root: %v", err)
	}
	rootAssistant := &storage.Message{SessionID: root.ID, Role: storage.RoleAssistant, Content: "root"}
	if err := store.Messages().Append(ctx, rootAssistant); err != nil {
		t.Fatalf("append root: %v", err)
	}
	firstFork := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local", ParentSessionID: root.ID, ForkFromMessageID: rootAssistant.ID}
	if err := store.Sessions().Create(ctx, firstFork); err != nil {
		t.Fatalf("create first fork: %v", err)
	}
	firstAssistant := &storage.Message{SessionID: firstFork.ID, Role: storage.RoleAssistant, Content: "first fork"}
	if err := store.Messages().Append(ctx, firstAssistant); err != nil {
		t.Fatalf("append first fork: %v", err)
	}
	secondFork := &storage.Session{OwnerID: "u", Platform: "cli", PlatformScopeID: "local", ParentSessionID: firstFork.ID, ForkFromMessageID: firstAssistant.ID}
	if err := store.Sessions().Create(ctx, secondFork); err != nil {
		t.Fatalf("create second fork: %v", err)
	}
	branch := &storage.Message{SessionID: secondFork.ID, Role: storage.RoleUser, Content: "second fork"}
	if err := store.Messages().Append(ctx, branch); err != nil {
		t.Fatalf("append second fork: %v", err)
	}

	loaded, err := (Loader{Store: store}).Load(ctx, secondFork.ID)
	if err != nil {
		t.Fatalf("load fork: %v", err)
	}
	got := messageContents(loaded.Messages)
	want := []string{"root", "first fork", "second fork"}
	if !equalStrings(got, want) {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
}

func messageContents(messages []storage.Message) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.Content)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
