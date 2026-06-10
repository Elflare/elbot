package contextmgr

import (
	"context"
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
	resolver := NewWindowResolver(
		config.ModelMetadataConfig{DefaultContextWindow: 8192, ContextWindows: map[string]int{"p/manual": 16000, "fallback": 12000}},
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
		t.Fatalf("manual model window = %d", got)
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
