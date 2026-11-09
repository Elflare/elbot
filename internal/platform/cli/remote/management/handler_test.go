package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"elbot/internal/control"
	"elbot/internal/workspace"
)

type handlerControlStub struct {
	ControlCapabilities
	result control.RespondConfirmationResult
}

func (s handlerControlStub) RespondConfirmation(context.Context, control.RespondConfirmationRequest) (control.RespondConfirmationResult, error) {
	return s.result, nil
}

type workspaceHandlerStub struct {
	ControlCapabilities
	workspace     control.WorkspaceResult
	directories   control.WorkspaceDirectoriesRequest
	git           control.WorkspaceGitRequest
	gitDiff       control.WorkspaceGitRequest
	userWorkspace control.SetUserWorkspaceRequest
	workspaceSync control.SetWorkspaceSyncRequest
}

func (s *workspaceHandlerStub) Workspace(context.Context) (control.WorkspaceResult, error) {
	return s.workspace, nil
}
func (s *workspaceHandlerStub) WorkspaceDirectories(_ context.Context, req control.WorkspaceDirectoriesRequest) (control.WorkspaceDirectoriesResult, error) {
	s.directories = req
	return workspace.DirectoryListing{Path: req.Path, Directories: []workspace.Directory{}}, nil
}
func (s *workspaceHandlerStub) SetUserWorkspace(_ context.Context, req control.SetUserWorkspaceRequest) (control.WorkspaceResult, error) {
	s.userWorkspace = req
	return s.workspace, nil
}
func (s *workspaceHandlerStub) SetWorkspaceSync(_ context.Context, req control.SetWorkspaceSyncRequest) (control.WorkspaceResult, error) {
	s.workspaceSync = req
	return s.workspace, nil
}
func (s *workspaceHandlerStub) WorkspaceGit(_ context.Context, req control.WorkspaceGitRequest) (control.WorkspaceGitResult, error) {
	s.git = req
	return control.WorkspaceGitResult{IsGit: true, Changes: []workspace.GitChange{}}, nil
}
func (s *workspaceHandlerStub) WorkspaceGitDiff(_ context.Context, req control.WorkspaceGitRequest) (string, error) {
	s.gitDiff = req
	return "diff", nil
}

func TestContinuationCallbackRunsAfterHTTPResponseIsWritten(t *testing.T) {
	response := httptest.NewRecorder()
	called := false
	handler := NewHandler(HandlerOptions{
		Control: handlerControlStub{result: control.RespondConfirmationResult{
			Accepted:     true,
			Continuation: &control.Continuation{Kind: "append", SessionID: "session"},
		}},
		Authenticate: func(ctx context.Context, _ *http.Request) (context.Context, bool, error) {
			return ctx, true, nil
		},
		Continue: func(_ context.Context, continuation control.Continuation) {
			called = true
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"accepted":true`) {
				t.Errorf("continuation called before response was written: status=%d body=%q", response.Code, response.Body.String())
			}
			if continuation.SessionID != "session" {
				t.Errorf("continuation=%+v", continuation)
			}
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/confirmation/respond", strings.NewReader(`{"decision":"approve","scope":"current"}`))
	handler.ServeHTTP(response, request)
	if !called {
		t.Fatal("continuation callback was not called")
	}
}

func TestWorkspaceManagementRoutesForwardRequests(t *testing.T) {
	stub := &workspaceHandlerStub{workspace: control.WorkspaceResult{SessionID: "s1", AgentPath: "/agent", UserPath: "/user"}}
	handler := NewHandler(HandlerOptions{
		Control: stub,
		Authenticate: func(ctx context.Context, _ *http.Request) (context.Context, bool, error) {
			return ctx, true, nil
		},
	})
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/v1/workspace"},
		{method: http.MethodGet, path: "/api/v1/workspace/directories?path=/home/user"},
		{method: http.MethodPut, path: "/api/v1/workspace/user", body: `{"path":"/home/user/project"}`},
		{method: http.MethodPut, path: "/api/v1/workspace/sync", body: `{"agent_to_user":true,"user_to_agent":false}`},
		{method: http.MethodGet, path: "/api/v1/workspace/git?side=user"},
		{method: http.MethodGet, path: "/api/v1/workspace/git/diff?side=agent&path=main.go"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		})
	}
	if stub.workspace.SessionID != "s1" || stub.directories.Path != "/home/user" || stub.userWorkspace.Path != "/home/user/project" || !stub.workspaceSync.AgentToUser || stub.workspaceSync.UserToAgent || stub.git.Side != "user" || stub.gitDiff.Side != "agent" || stub.gitDiff.Path != "main.go" {
		t.Fatalf("forwarded requests: %+v", stub)
	}
}
