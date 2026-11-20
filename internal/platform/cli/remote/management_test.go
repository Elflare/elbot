package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/operationresult"
	"elbot/internal/platform/cli/remote/management"
	"elbot/internal/platform/share"
)

type managementControlStub struct {
	management.ControlCapabilities
	role contextinfo.Role
}

func (s *managementControlStub) Status(ctx context.Context) (operationresult.Result[control.StatusResult], error) {
	actor, _ := contextinfo.ActorFromContext(ctx)
	s.role = actor.Role
	return operationresult.Result[control.StatusResult]{Status: operationresult.Success, Data: control.StatusResult{Mode: "work"}}, nil
}

func TestManagementAPIRequiresConnectionTokenAndMapsRole(t *testing.T) {
	t.Setenv("ELBOT_CLI_TEST_TOKEN", "secret")
	controller := &managementControlStub{}
	s := testRemoteServer(t)
	s.control = controller
	s.workCtx = context.Background()
	client := &remoteClientConn{id: "user", apiToken: "connection", mediaToken: "media"}
	s.addClient(client)
	defer s.removeClient(client)
	api := s.managementHandler()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	request.Header.Set("Authorization", "Bearer connection")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusOK || controller.role != contextinfo.RoleUser {
		t.Fatalf("status=%d role=%q body=%s", response.Code, controller.role, response.Body.String())
	}

	for _, method := range []string{"GET", "POST"} {
		for _, token := range []string{"", "Bearer secret", "Bearer media"} {
			request = httptest.NewRequest(method, "/api/v1/sessions", nil)
			request.Header.Set("X-Client-ID", "user")
			request.Header.Set("X-CLI-Connection", "connection")
			request.Header.Set("Authorization", token)
			response = httptest.NewRecorder()
			api.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("%s with %q: %d", method, token, response.Code)
			}
		}
	}
}

func TestManagementAPIRouterErrorsUseJSONEnvelope(t *testing.T) {
	s := testRemoteServer(t)
	s.control = &managementControlStub{}
	s.workCtx = context.Background()
	s.apiClients["connection"] = &remoteClientConn{id: "user"}
	api := s.managementHandler()
	for _, tc := range []struct {
		method string
		path   string
		status int
		code   string
	}{
		{method: http.MethodGet, path: "/api/v1/no-such-route", status: http.StatusNotFound, code: "route_not_found"},
		{method: http.MethodPost, path: "/api/v1/status", status: http.StatusMethodNotAllowed, code: "method_not_allowed"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			request.Header.Set("Authorization", "Bearer connection")
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Fatalf("content type=%q", got)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			if body.Error.Code != tc.code {
				t.Fatalf("error code=%q want=%q", body.Error.Code, tc.code)
			}
		})
	}
}

func TestManagementAPIResolvesSuperadminFromConfiguredIdentity(t *testing.T) {
	s := testRemoteServer(t)
	s.workCtx = context.Background()
	s.superadmins = share.NewSuperadmins(testPlatformName, []string{"user"})
	s.apiClients["connection"] = &remoteClientConn{id: "user"}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer connection")
	ctx, err := s.managementContext(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok || actor.Role != contextinfo.RoleSuperadmin || actor.ID != "user" {
		t.Fatalf("actor=%+v present=%v", actor, ok)
	}
}

func TestControlContinuationIsScheduledAsynchronously(t *testing.T) {
	s := testRemoteServer(t)
	s.workCtx = context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	s.SetControlContinuation(func(context.Context, control.Continuation) (context.Context, operationresult.Result[struct{}], error) {
		close(entered)
		<-release
		return context.Background(), operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	})
	s.scheduleControlContinuation(context.Background(), control.Continuation{Kind: "append", SessionID: "s"})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("continuation was not started")
	}
	close(release)
}
