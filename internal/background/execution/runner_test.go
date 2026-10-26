package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"elbot/internal/agent"
	background "elbot/internal/background/share"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/tool"
	"elbot/internal/workspace"
)

type backgroundAgentFunc func(context.Context, agent.BackgroundRequest) (agent.BackgroundResult, error)

func (f backgroundAgentFunc) RunBackground(ctx context.Context, req agent.BackgroundRequest) (agent.BackgroundResult, error) {
	return f(ctx, req)
}

func TestRunnerPreparesWorkspaceAndSource(t *testing.T) {
	for _, tc := range []struct {
		name, subdir, wantDir string
		kind                  background.Kind
		wantKind              contextinfo.BackgroundKind
	}{
		{name: "explicit", subdir: " tasks/report ", wantDir: "tasks/report", kind: background.KindCron, wantKind: contextinfo.BackgroundKindCron},
		{name: "cron", wantDir: "cron", kind: background.KindCron, wantKind: contextinfo.BackgroundKindCron},
		{name: "elnis", wantDir: "elnis", kind: background.KindElnis, wantKind: contextinfo.BackgroundKindElnis},
		{name: "custom", wantDir: "custom", kind: " custom ", wantKind: "custom"},
		{name: "default", subdir: " ", wantDir: "background"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			calls := 0
			runner := NewBackgroundRunner(BackgroundRunnerDeps{SandboxRoot: root, Execution: backgroundAgentFunc(func(ctx context.Context, req agent.BackgroundRequest) (agent.BackgroundResult, error) {
				calls++
				scope, ok := workspace.PathScopeFromContext(ctx)
				if !ok || scope.Dir != filepath.Join(root, tc.wantDir) || !scope.Restricted {
					t.Fatalf("scope=%+v present=%v", scope, ok)
				}
				info, err := os.Stat(scope.Dir)
				if err != nil || !info.IsDir() {
					t.Fatalf("task directory not prepared: %v", err)
				}
				if kind, _ := contextinfo.BackgroundKindFromContext(ctx); kind != tc.wantKind {
					t.Fatalf("kind=%q want=%q", kind, tc.wantKind)
				}
				return agent.BackgroundResult{}, nil
			})})
			ctx := workspace.WithPathScope(t.Context(), workspace.PathScope{Dir: t.TempDir()})
			_, err := runner.RunBackground(ctx, background.RunRequest{Kind: tc.kind, SandboxSubdir: tc.subdir})
			if err != nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestRunnerForwardsTaskAndPreservesExecutionResult(t *testing.T) {
	failure := errors.New("execution failed")
	for _, tc := range []struct {
		name, sessionID string
		allowed         []string
		result          agent.BackgroundResult
		err             error
	}{
		{name: "new unrestricted", result: agent.BackgroundResult{RunID: "run", SessionID: "new", MessageID: "message", Text: "answer", Outcome: "completed"}},
		{name: "continuation no local tools", sessionID: "existing", allowed: []string{}, result: agent.BackgroundResult{SessionID: "existing", TakenOver: true, Outcome: "taken_over"}},
		{name: "partial failure", sessionID: "existing", allowed: []string{"local"}, result: agent.BackgroundResult{RunID: "run", SessionID: "existing", Text: "partial", Outcome: "failed"}, err: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := background.RunRequest{
				Kind: background.KindElnis, Name: "task", Title: "title", Platform: "qq", ScopeID: "scope", SessionID: tc.sessionID,
				Actor:         contextinfo.Actor{ID: "actor", Platform: "qq", Role: contextinfo.RoleUser},
				ModelProvider: "provider", Model: "model", Prompt: "before after", NeedsElyphRules: true,
				PromptSegments: []llm.MessageSegment{{Type: llm.SegmentText, Text: "before "}, {Type: llm.SegmentImage, MediaID: "image"}, {Type: llm.SegmentText, Text: "after"}},
				ToolListNames:  []string{"skill"}, AllowedToolNames: tc.allowed,
				ELwispTools: []tool.ELwispToolDeclaration{{Name: "remote", CanonicalName: "origin_remote", EventKey: "event", Endpoint: "https://example.invalid"}},
				Metadata:    map[string]string{"source": "event"},
			}
			want := agent.BackgroundRequest{
				Kind: "elnis", Name: "task", Title: "title", Platform: "qq", ScopeID: "scope", SessionID: tc.sessionID,
				Actor: req.Actor, ModelProvider: "provider", Model: "model", Prompt: "before after", NeedsElyphRules: true,
				PromptSegments: req.PromptSegments, ToolListNames: req.ToolListNames, AllowedToolNames: tc.allowed,
				ELwispTools: req.ELwispTools, Metadata: req.Metadata,
			}
			calls := 0
			runner := NewBackgroundRunner(BackgroundRunnerDeps{SandboxRoot: t.TempDir(), Execution: backgroundAgentFunc(func(_ context.Context, got agent.BackgroundRequest) (agent.BackgroundResult, error) {
				calls++
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("request=%+v want=%+v", got, want)
				}
				return tc.result, tc.err
			})})
			got, err := runner.RunBackground(t.Context(), req)
			wantResult := background.RunResult{RunID: tc.result.RunID, SessionID: tc.result.SessionID, MessageID: tc.result.MessageID, Text: tc.result.Text, TakenOver: tc.result.TakenOver, Outcome: tc.result.Outcome}
			if calls != 1 || !errors.Is(err, tc.err) || !reflect.DeepEqual(got, wantResult) {
				t.Fatalf("calls=%d result=%+v err=%v", calls, got, err)
			}
		})
	}
}

func TestRunnerPreservesCallerCancellationAndValues(t *testing.T) {
	type callerKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), callerKey{}, "caller"))
	defer cancel()
	runner := NewBackgroundRunner(BackgroundRunnerDeps{SandboxRoot: t.TempDir(), Execution: backgroundAgentFunc(func(ctx context.Context, _ agent.BackgroundRequest) (agent.BackgroundResult, error) {
		if ctx.Value(callerKey{}) != "caller" {
			t.Fatal("lost caller context")
		}
		cancel()
		if ctx.Err() != context.Canceled {
			t.Fatal("caller cancellation not propagated")
		}
		return agent.BackgroundResult{SessionID: "prepared"}, ctx.Err()
	})})
	result, err := runner.RunBackground(ctx, background.RunRequest{})
	if !errors.Is(err, context.Canceled) || result.SessionID != "prepared" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunnerRejectsWorkspaceFailureBeforeAgent(t *testing.T) {
	for _, subdir := range []string{"../escape", "blocked/child"} {
		t.Run(subdir, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "blocked"), []byte("file"), 0600); err != nil {
				t.Fatal(err)
			}
			runner := NewBackgroundRunner(BackgroundRunnerDeps{SandboxRoot: root, Execution: backgroundAgentFunc(func(context.Context, agent.BackgroundRequest) (agent.BackgroundResult, error) {
				t.Fatal("Agent called before successful workspace preparation")
				return agent.BackgroundResult{}, nil
			})})
			result, err := runner.RunBackground(t.Context(), background.RunRequest{SandboxSubdir: subdir})
			if err == nil || !reflect.DeepEqual(result, background.RunResult{}) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}
