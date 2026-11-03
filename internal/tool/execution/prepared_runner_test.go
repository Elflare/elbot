package execution

import (
	"context"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

type snapshotConfirmDeps struct {
	runnerTestDeps
	change func()
}

func (d *snapshotConfirmDeps) ConfirmToolCall(context.Context, string, llm.ToolCallRequest, share.RiskAssessment, string) (share.ConfirmResult, error) {
	d.confirmed = true
	d.change()
	return share.ConfirmResult{Allowed: true}, nil
}

func TestRunKeepsSelectedObjectAcrossCatalogChanges(t *testing.T) {
	for _, action := range []string{"replace", "remove"} {
		t.Run(action, func(t *testing.T) {
			permission := security.DefaultPermission()
			catalog := registry.NewRegistry(permission)
			original := &snapshotTool{}
			replacement := &snapshotTool{}
			if err := registerTestTools(catalog, original.Definition()); err != nil {
				t.Fatal(err)
			}
			deps := &snapshotConfirmDeps{change: func() {
				var err error
				if action == "replace" {
					_, err = catalog.RegisterTools(t.Context(), share.SourceSkill, share.ToolRegistrationScope{}, []share.ToolDefinition{replacement.Definition()})
				} else {
					_, err = catalog.RegisterTools(t.Context(), share.SourceSkill, share.ToolRegistrationScope{}, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			manager := newTestManager(catalog, permission)
			req := share.RunRequest{Committer: &testToolCommitter{}, Session: &storage.Session{ID: "snapshot", Mode: storage.SessionModeWork}, Actor: contextinfo.Actor{Role: contextinfo.RoleSuperadmin}, Calls: []llm.ToolCallRequest{{ID: "one", Name: original.Name(), Arguments: "{}"}}}
			result := manager.Run(t.Context(), deps, req)
			if result.Err != nil || !deps.confirmed || original.calls != 1 || original.assessments != 1 || replacement.calls != 0 {
				t.Fatalf("result=%+v old=%+v new=%+v", result, original, replacement)
			}
			committer := &testToolCommitter{}
			req.Committer = committer
			result = manager.Run(t.Context(), &runnerTestDeps{}, req)
			if result.Err != nil || original.calls != 1 || original.assessments != 1 {
				t.Fatalf("old object reused: %+v", original)
			}
			if action == "replace" && (replacement.calls != 1 || replacement.assessments != 1) {
				t.Fatalf("new object not selected: %+v", replacement)
			}
			if action == "remove" && (len(committer.messages) != 1 || llm.SegmentsContentText(committer.messages[0].Segments) == "original") {
				t.Fatalf("removed object still callable: %+v", result)
			}
		})
	}
}
