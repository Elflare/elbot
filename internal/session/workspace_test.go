package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"elbot/internal/storage"
	"elbot/internal/workspace"
)

func TestWorkspaceMutationsPreserveConcurrentFields(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "owner", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	row, err := svc.Create(ctx, scope, CreateRequest{Metadata: `{"unknown":9007199254740993,"workspace_dir":"/initial","updates":0}`})
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewWorkspaceStore(svc, store.Sessions(), row.ID)
	const count = 16
	failures := make(chan error, count*2)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(2)
		go func() {
			defer wg.Done()
			failures <- adapter.MarkWorkspaceAgentNoticeDir(ctx, fmt.Sprintf("/workspace/%d", i))
		}()
		go func() {
			defer wg.Done()
			_, err := store.Sessions().Mutate(ctx, row.ID, func(latest *storage.Session) error {
				fields, err := storage.DecodeSessionMetadata(latest.Metadata)
				if err != nil {
					return err
				}
				var n int
				if err := json.Unmarshal(fields["updates"], &n); err != nil {
					return err
				}
				if err := fields.Set("updates", n+1); err != nil {
					return err
				}
				latest.Metadata, err = fields.Encode()
				return err
			})
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	latest, err := store.Sessions().Get(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := storage.DecodeSessionMetadata(latest.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if string(fields["unknown"]) != "9007199254740993" || string(fields["updates"]) != "16" {
		t.Fatalf("lost unrelated fields: %s", latest.Metadata)
	}
	state, err := workspace.DecodeState(latest.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if state.Dir != "/initial" || len(state.AgentNoticeDirs) != count {
		t.Fatalf("workspace=%+v", state)
	}
	if err := adapter.SetWorkspaceDirWithAgentNotice(ctx, "/selected", true); err != nil {
		t.Fatal(err)
	}
	if err := adapter.EnsureWorkspaceDir(ctx, "/background"); err != nil {
		t.Fatal(err)
	}
	dir, err := adapter.GetWorkspaceDir(ctx)
	if err != nil || dir != "/selected" {
		t.Fatalf("background overwrote selected workspace: %q %v", dir, err)
	}
}

func TestWorkspaceRejectsMalformedMetadataAndExpiredBinding(t *testing.T) {
	for _, raw := range []string{`{`, `null`, `{"workspace_dir":42,"unknown":1}`, `{"workspace_agent_notice_dirs":"bad"}`} {
		t.Run(raw, func(t *testing.T) {
			svc, store := newTestService(t)
			ctx := context.Background()
			row := &storage.Session{OwnerID: "owner", Platform: "cli", PlatformScopeID: "local", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive, Metadata: raw}
			if err := store.Sessions().Create(ctx, row); err != nil {
				t.Fatal(err)
			}
			adapter := NewWorkspaceStore(svc, store.Sessions(), row.ID)
			if _, err := adapter.GetWorkspaceDir(ctx); err == nil {
				t.Fatal("accepted corrupt workspace")
			}
			if err := adapter.SetWorkspaceDir(ctx, "/next"); err == nil {
				t.Fatal("overwrote corrupt metadata")
			}
			latest, err := store.Sessions().Get(ctx, row.ID)
			if err != nil || latest.Metadata != raw {
				t.Fatalf("metadata=%+v err=%v", latest, err)
			}
		})
	}
	svc, store := newTestService(t)
	ctx := context.Background()
	scope := Scope{ActorID: "owner", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	row, err := svc.Create(ctx, scope, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, binding, err := svc.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewWorkspaceStore(svc, store.Sessions(), row.ID)
	old := WithBinding(ctx, binding)
	if _, err := svc.Create(ctx, scope, CreateRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resume(ctx, scope, row.ID); err != nil {
		t.Fatal(err)
	}
	if err := adapter.SetWorkspaceDir(old, "/stale"); err == nil {
		t.Fatal("expired call changed workspace")
	}
	if _, err := adapter.GetWorkspaceDir(old); err == nil {
		t.Fatal("expired read revived")
	}
	_, fresh, err := svc.CurrentBound(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.SetWorkspaceDir(WithBinding(ctx, fresh), "/fresh"); err != nil {
		t.Fatal(err)
	}
}
