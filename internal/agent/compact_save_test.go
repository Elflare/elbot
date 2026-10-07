package agent

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type compactSaveStore struct {
	storage.Store
	repo *compactSaveRepo
}

func (s compactSaveStore) Sessions() storage.SessionRepository { return s.repo }

type compactSaveRepo struct {
	storage.SessionRepository
	failure error
}

func (r *compactSaveRepo) Create(ctx context.Context, row *storage.Session) error {
	if r.failure != nil {
		return r.failure
	}
	return r.SessionRepository.Create(ctx, row)
}

func TestCompactSaveFailureReleasesExecutionAndKeepsSource(t *testing.T) {
	for _, backgroundRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreground", true: "background"}[backgroundRun], func(t *testing.T) {
			ctx := context.Background()
			base := newTestStore(t)
			repo := &compactSaveRepo{SessionRepository: base.Sessions()}
			store := compactSaveStore{Store: base, repo: repo}
			a := newTestAgent(t, &fakePlatform{}, &fakeLLM{replies: []string{"compressed history"}}, "model", config.ProviderConfig{}, store)
			var source *storage.Session
			var err error
			if backgroundRun {
				source, err = a.execution.sessions.PrepareBackground(ctx, session.Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "cron:save"}, session.BackgroundRequest{Kind: "cron", Name: "save"})
			} else {
				source, err = a.execution.sessions.Create(ctx, a.identity.Scope(ctx), session.CreateRequest{Title: "source", Metadata: `{"llm_origin":{"protocol":"chat"}}`})
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, message := range []*storage.Message{{SessionID: source.ID, Role: storage.RoleUser, Content: "history"}, {SessionID: source.ID, Role: storage.RoleAssistant, Content: "answer"}} {
				if err := base.Messages().Append(ctx, message); err != nil {
					t.Fatal(err)
				}
			}
			a.execution.contexts.Configure(config.ContextConfig{CompactEnabled: true, CompactTriggerRatio: .8}, config.ModelMetadataConfig{DefaultContextWindow: 100}, nil)
			a.execution.recordUsage(t.Context(), source.ID, &llm.Usage{TotalTokens: 80})
			failure := errors.New("compact save rejected")
			repo.failure = failure
			if backgroundRun {
				_, err = a.RunBackground(ctx, background.RunRequest{SessionID: source.ID, Kind: background.KindCron, Name: "save", Platform: "cli", Actor: contextinfo.Actor{ID: "cli:local", Platform: "cli", PlatformUserID: "local", Role: contextinfo.RoleSuperadmin}, Prompt: "next input"})
			} else {
				err = a.HandleMessage(ctx, "/compact")
			}
			if !errors.Is(err, failure) {
				t.Fatalf("compact error=%v", err)
			}
			if len(a.execution.requests.List()) != 0 || a.execution.turns.Snapshot(source.ID).Phase != turn.PhaseIdle {
				t.Fatal("failed compact retained execution")
			}
			current, err := a.execution.sessions.Current(ctx, a.identity.Scope(ctx))
			if backgroundRun {
				if !errors.Is(err, storage.ErrNotFound) {
					t.Fatalf("failed background compact activated current: %+v %v", current, err)
				}
			} else if err != nil || current.ID != source.ID {
				t.Fatalf("source binding changed: %+v %v", current, err)
			}
			messages, err := base.Messages().ListBySession(ctx, source.ID)
			if err != nil || len(messages) != 2 {
				t.Fatalf("source history changed: %+v %v", messages, err)
			}
		})
	}
}
