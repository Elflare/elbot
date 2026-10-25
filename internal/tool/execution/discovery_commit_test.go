package execution

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/discovery"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
	"elbot/internal/tool/state"
)

type discoveryCommitter struct {
	testToolCommitter
	t       *testing.T
	session *storage.Session
	before  string
	failure error
	patch   storage.SessionMetadata
}

func (c *discoveryCommitter) Result(ctx context.Context, index int, call llm.ToolCallRequest, message llm.LLMMessage, commit *storage.ToolResultCommit) (events.LogResult, error) {
	c.t.Helper()
	if c.session.Metadata != c.before {
		c.t.Fatal("session state changed before result commit")
	}
	if commit.Result == nil || commit.Record == nil || len(commit.Metadata) == 0 {
		c.t.Fatalf("discovery not submitted with result: %+v", commit)
	}
	c.patch = commit.Metadata
	if c.failure != nil {
		return events.LogResult{ResultStatus: events.ResultFailed}, c.failure
	}
	return c.testToolCommitter.Result(ctx, index, call, message, commit)
}

func TestDiscoveryStateTravelsWithResultAndAdvancesAfterCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			permission := security.DefaultPermission()
			catalog := registry.NewRegistry(permission)
			for _, candidate := range []share.Tool{fakeTool{name: "new", risk: share.RiskLow}, discovery.NewDiscoverTool(discovery.NewResolver(catalog), nil)} {
				if err := catalog.Register(candidate); err != nil {
					t.Fatal(err)
				}
			}
			before := `{"foreign":9007199254740993,"discovered_tools":[{"name":"old","source":"native","schema":{"name":"old"}}]}`
			row := &storage.Session{ID: "discovery", Mode: storage.SessionModeWork, Metadata: before}
			sink := &discoveryCommitter{t: t, session: row, before: before}
			if fail {
				sink.failure = errors.New("commit refused")
			}
			actor := contextinfo.Actor{Role: contextinfo.RoleSuperadmin}
			result := newTestManager(catalog, permission).Run(contextinfo.WithActor(t.Context(), actor), &runnerTestDeps{}, share.RunRequest{
				Session: row, Actor: actor, Committer: sink,
				Calls: []llm.ToolCallRequest{{ID: "discover", Name: "discover_tool", Arguments: `{"name":"new"}`}},
			})
			if !errors.Is(result.Err, sink.failure) {
				t.Fatalf("result=%+v", result)
			}
			if _, exists := sink.patch["foreign"]; exists {
				t.Fatal("tool patch contains unrelated field")
			}
			raw, err := sink.patch.Encode()
			if err != nil {
				t.Fatal(err)
			}
			updated, err := state.DecodeState(raw)
			if err != nil || len(updated.DiscoveredTools) != 2 || updated.DiscoveredTools[0].Name != "new" || updated.DiscoveredTools[1].Name != "old" {
				t.Fatalf("patch=%s err=%v", raw, err)
			}
			if fail {
				if row.Metadata != before {
					t.Fatal("failed commit changed session snapshot")
				}
			} else if !strings.Contains(row.Metadata, "9007199254740993") || !strings.Contains(row.Metadata, `"name":"new"`) {
				t.Fatalf("committed snapshot=%s", row.Metadata)
			}
		})
	}
}
