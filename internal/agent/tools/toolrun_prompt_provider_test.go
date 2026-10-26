package tools

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"errors"
	"reflect"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/session"
	"elbot/internal/storage"
	toolshare "elbot/internal/tool"
)

type promptQueryFunc func(context.Context, toolshare.ToolQuery) ([]toolshare.Info, error)

func (f promptQueryFunc) QueryTools(ctx context.Context, query toolshare.ToolQuery) ([]toolshare.Info, error) {
	return f(ctx, query)
}

type promptIdentity struct {
	actor contextinfo.Actor
}

func (i promptIdentity) Actor(context.Context) contextinfo.Actor { return i.actor }

func TestPromptNamesUseQueriedGroups(t *testing.T) {
	actor := contextinfo.Actor{ID: "test", Role: contextinfo.RoleUser}
	var kinds []string
	provider := toolRunPromptProvider{
		identity: promptIdentity{actor: actor},
		tools: promptQueryFunc(func(ctx context.Context, query toolshare.ToolQuery) ([]toolshare.Info, error) {
			got, _ := contextinfo.ActorFromContext(ctx)
			if got != actor {
				t.Fatalf("query actor = %#v, want %#v", got, actor)
			}
			if query.IncludeHidden || query.IncludeDiscover || query.Names != nil {
				t.Fatalf("unexpected query flags: %#v", query)
			}
			kinds = append(kinds, query.Kind)
			// Source must not override the classification returned by the query.
			if query.Kind == "tool" {
				return []toolshare.Info{{Name: "command", Source: toolshare.SourceSkillGo}}, nil
			}
			return []toolshare.Info{{Name: "guide", Source: toolshare.SourceBuiltin}}, nil
		}),
	}
	got, err := provider.ToolNames(context.Background(), storage.SessionModeWork, &storage.Session{}, session.Scope{})
	want := agentshare.PromptToolNames{Tools: []string{"command"}, Skills: []string{"guide"}}
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(kinds, []string{"tool", "skill"}) {
		t.Fatalf("names = %#v, queries = %v, error = %v", got, kinds, err)
	}
}

func TestPromptNamesQueryErrorsAndModes(t *testing.T) {
	failure := errors.New("query failed")
	for _, failedKind := range []string{"tool", "skill"} {
		t.Run(failedKind, func(t *testing.T) {
			provider := toolRunPromptProvider{
				identity: promptIdentity{},
				tools: promptQueryFunc(func(_ context.Context, query toolshare.ToolQuery) ([]toolshare.Info, error) {
					if query.Kind == failedKind {
						return nil, failure
					}
					return []toolshare.Info{{Name: "partial"}}, nil
				}),
			}
			got, err := provider.ToolNames(context.Background(), storage.SessionModeWork, &storage.Session{}, session.Scope{})
			if !errors.Is(err, failure) || len(got.Tools)+len(got.Skills) != 0 {
				t.Fatalf("names = %#v, error = %v", got, err)
			}
		})
	}
	// Inactive modes and cancellation must return before querying or resolving identity.
	provider := toolRunPromptProvider{}
	for _, mode := range []string{storage.SessionModeChat, storage.SessionModeBackground} {
		got, err := provider.ToolNames(context.Background(), mode, &storage.Session{}, session.Scope{})
		if err != nil || len(got.Tools)+len(got.Skills) != 0 {
			t.Fatalf("mode %s: names = %#v, error = %v", mode, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	got, err := provider.ToolNames(ctx, storage.SessionModeWork, &storage.Session{Metadata: `{"background_kind":"cron"}`}, session.Scope{})
	if err != nil || len(got.Tools)+len(got.Skills) != 0 {
		t.Fatalf("background session: names = %#v, error = %v", got, err)
	}
	cancel()
	if _, err := provider.ToolNames(ctx, storage.SessionModeWork, nil, session.Scope{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query error = %v", err)
	}
}
