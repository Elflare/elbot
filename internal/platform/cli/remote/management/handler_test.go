package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"elbot/internal/control"
	"elbot/internal/operationresult"
	"elbot/internal/tool"
	"elbot/internal/workspace"
)

type handlerControlStub struct {
	ControlCapabilities
	result  control.RespondConfirmationResult
	current control.CurrentConfirmationResult
}

func (s handlerControlStub) RespondConfirmation(context.Context, control.RespondConfirmationRequest) (operationresult.Result[control.RespondConfirmationResult], error) {
	return operationresult.Result[control.RespondConfirmationResult]{Status: operationresult.Success, Data: s.result}, nil
}

func (s handlerControlStub) CurrentConfirmation(context.Context, control.CurrentConfirmationRequest) (operationresult.Result[control.CurrentConfirmationResult], error) {
	return operationresult.Result[control.CurrentConfirmationResult]{Status: operationresult.Success, Data: s.current}, nil
}

type workspaceHandlerStub struct {
	ControlCapabilities
	workspace     control.WorkspaceResult
	files         control.WorkspaceFilesRequest
	directories   control.WorkspaceDirectoriesRequest
	git           control.WorkspaceGitRequest
	gitDiff       control.WorkspaceGitRequest
	userWorkspace control.SetUserWorkspaceRequest
	workspaceSync control.SetWorkspaceSyncRequest
}

func (s *workspaceHandlerStub) Workspace(context.Context) (operationresult.Result[control.WorkspaceResult], error) {
	return operationresult.Result[control.WorkspaceResult]{Status: operationresult.Success, Data: s.workspace}, nil
}
func (s *workspaceHandlerStub) WorkspaceFiles(_ context.Context, req control.WorkspaceFilesRequest) (operationresult.Result[control.WorkspaceFilesResult], error) {
	s.files = req
	return operationresult.Result[control.WorkspaceFilesResult]{Status: operationresult.Success, Data: workspace.WorkspaceFileListing{Side: req.Side, Path: req.Path, Items: []workspace.WorkspaceFile{}}}, nil
}
func (s *workspaceHandlerStub) WorkspaceDirectories(_ context.Context, req control.WorkspaceDirectoriesRequest) (operationresult.Result[control.WorkspaceDirectoriesResult], error) {
	s.directories = req
	return operationresult.Result[control.WorkspaceDirectoriesResult]{Status: operationresult.Success, Data: workspace.DirectoryListing{Path: req.Path, Directories: []workspace.Directory{}}}, nil
}
func (s *workspaceHandlerStub) SetUserWorkspace(_ context.Context, req control.SetUserWorkspaceRequest) (operationresult.Result[control.WorkspaceResult], error) {
	s.userWorkspace = req
	return operationresult.Result[control.WorkspaceResult]{Status: operationresult.Success, Data: s.workspace}, nil
}
func (s *workspaceHandlerStub) SetWorkspaceSync(_ context.Context, req control.SetWorkspaceSyncRequest) (operationresult.Result[control.WorkspaceResult], error) {
	s.workspaceSync = req
	return operationresult.Result[control.WorkspaceResult]{Status: operationresult.Success, Data: s.workspace}, nil
}
func (s *workspaceHandlerStub) WorkspaceGit(_ context.Context, req control.WorkspaceGitRequest) (operationresult.Result[control.WorkspaceGitResult], error) {
	s.git = req
	return operationresult.Result[control.WorkspaceGitResult]{Status: operationresult.Success, Data: control.WorkspaceGitResult{IsGit: true, Changes: []workspace.GitChange{}}}, nil
}
func (s *workspaceHandlerStub) WorkspaceGitDiff(_ context.Context, req control.WorkspaceGitRequest) (operationresult.Result[string], error) {
	s.gitDiff = req
	return operationresult.Result[string]{Status: operationresult.Success, Data: "diff"}, nil
}
func (s *workspaceHandlerStub) LoadToolDetail(context.Context, control.ToolDetailRequest) (operationresult.Result[tool.DetailBlock], error) {
	return operationresult.Result[tool.DetailBlock]{Status: operationresult.Success, Data: tool.DetailBlock{Content: "# Detail", Format: "markdown"}}, nil
}

func TestWriteControlResultErrorMapsBusinessStatuses(t *testing.T) {
	tests := []struct {
		status operationresult.Status
		http   int
		code   string
	}{
		{operationresult.Rejected, http.StatusBadRequest, "operation_rejected"},
		{operationresult.Failed, http.StatusInternalServerError, "operation_failed"},
		{operationresult.InvalidArguments, http.StatusBadRequest, "invalid_arguments"},
		{operationresult.Forbidden, http.StatusForbidden, "permission_denied"},
		{operationresult.NoCurrentSession, http.StatusNotFound, "no_current_session"},
		{operationresult.NotFound, http.StatusNotFound, "not_found"},
		{operationresult.Conflict, http.StatusConflict, "conflict"},
		{operationresult.Unavailable, http.StatusServiceUnavailable, "unavailable"},
		{operationresult.Busy, http.StatusConflict, "busy"},
		{operationresult.Timeout, http.StatusGatewayTimeout, "timeout"},
		{operationresult.Canceled, http.StatusRequestTimeout, "canceled"},
		{operationresult.InvalidTarget, http.StatusBadRequest, "invalid_target"},
		{operationresult.Incompatible, http.StatusConflict, "incompatible"},
	}
	for _, test := range tests {
		t.Run(string(test.status), func(t *testing.T) {
			response := httptest.NewRecorder()
			handled := writeControlResultError(response, operationresult.Result[struct{}]{Status: test.status, Message: "business message"}, nil)
			if !handled || response.Code != test.http {
				t.Fatalf("handled/status = %v/%d, want true/%d", handled, response.Code, test.http)
			}
			var body map[string]map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error"]["code"] != test.code || body["error"]["message"] != "business message" {
				t.Fatalf("error = %+v", body["error"])
			}
		})
	}
}

func TestWriteControlResultErrorPreservesGoErrorAndRejectsUnknownStatus(t *testing.T) {
	response := httptest.NewRecorder()
	if !writeControlResultError(response, operationresult.Result[struct{}]{Status: operationresult.Success}, context.DeadlineExceeded) {
		t.Fatal("Go error was not handled")
	}
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), context.DeadlineExceeded.Error()) {
		t.Fatalf("Go error response = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handled := writeControlResultError(response, operationresult.Result[struct{}]{Status: "unknown", Message: "ignored"}, nil)
	var body map[string]map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !handled || response.Code != http.StatusInternalServerError || body["error"]["code"] != "internal_error" {
		t.Fatalf("unknown status response = handled:%v status:%d body:%s", handled, response.Code, response.Body.String())
	}
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
		{method: http.MethodGet, path: "/api/v1/workspace/files?side=user&path=notes"},
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
	if stub.workspace.SessionID != "s1" || stub.directories.Path != "/home/user" || stub.files.Side != "user" || stub.files.Path != "notes" || stub.userWorkspace.Path != "/home/user/project" || !stub.workspaceSync.AgentToUser || stub.workspaceSync.UserToAgent || stub.git.Side != "user" || stub.gitDiff.Side != "agent" || stub.gitDiff.Path != "main.go" {
		t.Fatalf("forwarded requests: %+v", stub)
	}
}

func TestToolDetailRouteReturnsCompleteDetail(t *testing.T) {
	response := httptest.NewRecorder()
	handler := NewHandler(HandlerOptions{
		Control: &workspaceHandlerStub{},
		Authenticate: func(ctx context.Context, _ *http.Request) (context.Context, bool, error) {
			return ctx, true, nil
		},
	})
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tools/guide/detail", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"content":"# Detail"`) || !strings.Contains(response.Body.String(), `"format":"markdown"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCurrentConfirmationRouteReturnsStructuredDetail(t *testing.T) {
	response := httptest.NewRecorder()
	handler := NewHandler(HandlerOptions{
		Control: handlerControlStub{current: control.CurrentConfirmationResult{
			Kind: "tool",
			Detail: []tool.ConfirmationDetailBlock{
				{Kind: tool.ConfirmationDetailDiff, Content: "-before\n+after"},
				{Kind: tool.ConfirmationDetailArguments, Content: map[string]any{"operation": "write"}},
			},
		}},
		Authenticate: func(ctx context.Context, _ *http.Request) (context.Context, bool, error) {
			return ctx, true, nil
		},
	})
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/confirmation", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"kind":"diff"`) || !strings.Contains(response.Body.String(), `"content":{"operation":"write"}`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
