package control

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/session/share"
	"elbot/internal/storage"
	"elbot/internal/workspace"
)

func TestWorkspaceAndOriginPreserveConcurrentMetadata(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx, scope := context.Background(), privateScope()
	row := f.create(t, scope, "one")
	if _, err := f.store.Sessions().Mutate(ctx, row.ID, func(row *storage.Session) error {
		row.Metadata = `{"unknown":9007199254740993,"workspace_dir":"/initial"}`
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := range 16 {
		workers.Go(func() {
			if err := f.s.MarkWorkspaceAgentNoticeDir(ctx, row.ID, fmt.Sprintf("/work/%d", i)); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Go(func() {
		if result, err := f.s.Rename(ctx, scope, row.ID, "renamed"); err != nil || result.Status != operationresult.Success {
			t.Errorf("rename: %+v %v", result, err)
		}
	})
	workers.Go(func() {
		if _, err := f.s.RegisterOrigin(ctx, row.ID, llm.Origin{APIType: "protocol", Provider: "original"}); err != nil {
			t.Error(err)
		}
	})
	workers.Wait()
	stored, err := f.store.Sessions().Get(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := storage.DecodeSessionMetadata(stored.Metadata)
	if err != nil || string(fields["unknown"]) != "9007199254740993" || string(fields["title_renamed"]) != "true" {
		t.Fatalf("lost metadata: %s %v", stored.Metadata, err)
	}
	state, err := workspace.DecodeState(stored.Metadata)
	if err != nil || len(state.AgentNoticeDirs) != 16 {
		t.Fatalf("workspace state: %+v %v", state, err)
	}
	if err := f.s.SetWorkspaceDirWithAgentNotice(ctx, row.ID, "/selected", true); err != nil {
		t.Fatal(err)
	}
	if err := f.s.EnsureWorkspaceDir(ctx, row.ID, "/background"); err != nil {
		t.Fatal(err)
	}
	if cleared, err := f.s.ClearWorkspaceDirIfMatches(ctx, row.ID, "/initial"); err != nil || cleared {
		t.Fatalf("stale clear: %v %v", cleared, err)
	}
	if dir, err := f.s.GetWorkspaceDir(ctx, row.ID); err != nil || dir != "/selected" {
		t.Fatalf("selected dir: %s %v", dir, err)
	}
	if noticed, err := f.s.HasWorkspaceAgentNoticeDir(ctx, row.ID, "/selected"); err != nil || !noticed {
		t.Fatalf("notice: %v %v", noticed, err)
	}
	if _, err := f.s.RegisterOrigin(ctx, row.ID, llm.Origin{APIType: "other", Provider: "new"}); err == nil {
		t.Fatal("historical protocol reinterpreted")
	}
	stored, err = f.s.RegisterOrigin(ctx, row.ID, llm.Origin{APIType: "protocol", Provider: "new"})
	if err != nil {
		t.Fatal(err)
	}
	origin, _, err := share.Origin(stored)
	if err != nil || origin.Provider != "original" {
		t.Fatalf("historical provider replaced: %+v %v", origin, err)
	}
	oldCtx := share.WithBinding(ctx, f.s.CurrentBinding(scope))
	f.create(t, scope, "two")
	if err := f.s.EnsureWorkspaceDir(oldCtx, row.ID, "/stale"); err == nil {
		t.Fatal("old binding wrote workspace")
	}
	if _, err := f.s.GetWorkspaceDir(oldCtx, row.ID); err == nil {
		t.Fatal("old binding read workspace")
	}
	if _, err := f.s.RegisterOrigin(oldCtx, row.ID, llm.Origin{APIType: "protocol", Provider: "new"}); err == nil {
		t.Fatal("old binding registered origin")
	}
}

func TestWorkspaceSyncDirectionsFollowTheChangedSide(t *testing.T) {
	f := newFixture(t, IdleExpirationConfig{})
	ctx := context.Background()
	row := f.create(t, privateScope(), "one")
	if _, err := f.store.Sessions().Mutate(ctx, row.ID, func(row *storage.Session) error {
		row.Metadata = `{"workspace_agent_to_user":false}`
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetWorkspaceDirWithAgentNotice(ctx, row.ID, "/agent", false); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetUserWorkspaceDir(ctx, row.ID, "/user"); err != nil {
		t.Fatal(err)
	}
	state, err := f.s.SetWorkspaceSync(ctx, row.ID, true, false, "/agent")
	if err != nil || state.Dir != "/agent" || state.UserDir != "/agent" {
		t.Fatalf("enable agent-to-user = %+v, %v", state, err)
	}
	if err := f.s.SetWorkspaceDirWithAgentNotice(ctx, row.ID, "/agent-next", false); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetUserWorkspaceDir(ctx, row.ID, "/user-only"); err != nil {
		t.Fatal(err)
	}
	state, err = f.s.GetWorkspaceState(ctx, row.ID)
	if err != nil || state.Dir != "/agent-next" || state.UserDir != "/user-only" {
		t.Fatalf("one-way state = %+v, %v", state, err)
	}
	state, err = f.s.SetWorkspaceSync(ctx, row.ID, true, true, "/agent-next")
	if err != nil || state.Dir != "/user-only" || state.UserDir != "/user-only" {
		t.Fatalf("enable reverse direction = %+v, %v", state, err)
	}
	if err := f.s.SetUserWorkspaceDir(ctx, row.ID, "/both-user"); err != nil {
		t.Fatal(err)
	}
	state, err = f.s.GetWorkspaceState(ctx, row.ID)
	if err != nil || state.Dir != "/both-user" || state.UserDir != "/both-user" {
		t.Fatalf("user change = %+v, %v", state, err)
	}
	if err := f.s.SetWorkspaceDirWithAgentNotice(ctx, row.ID, "/both-agent", false); err != nil {
		t.Fatal(err)
	}
	state, err = f.s.GetWorkspaceState(ctx, row.ID)
	if err != nil || state.Dir != "/both-agent" || state.UserDir != "/both-agent" {
		t.Fatalf("agent change = %+v, %v", state, err)
	}
}
