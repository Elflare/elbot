package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/operationresult"
	"elbot/internal/platform/cli/remote/management"
	clishare "elbot/internal/platform/cli/share"
)

type ControlCapabilities = management.ControlCapabilities

type managementAPIClientContextKey struct{}

func (s *RemoteServer) managementHandler() http.Handler {
	handler := management.NewHandler(management.HandlerOptions{
		Control: s.control,
		SelectSession: func(ctx context.Context, id string) {
			c := ctx.Value(managementAPIClientContextKey{}).(*remoteClientConn)
			s.mu.Lock()
			c.sessionID = id
			c.coagentView = clishare.CoagentView{}
			s.mu.Unlock()
		},
		Authenticate: s.managementContext,
		Continue:     s.scheduleControlContinuation,
	})
	return s.apiMiddleware(handler)
}

func (s *RemoteServer) apiMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			writeManagementStopping(w)
			return
		}
		s.work.Add(1)
		s.mu.Unlock()
		defer s.work.Done()

		ctx, cancel := context.WithCancel(req.Context())
		stopWork := context.AfterFunc(s.workCtx, cancel)
		defer func() { stopWork(); cancel() }()
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

func (s *RemoteServer) managementContext(ctx context.Context, req *http.Request) (context.Context, error) {
	token, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	client := s.apiClients[token]
	s.mu.Unlock()
	if !ok || client == nil {
		return nil, errors.New("invalid connection token")
	}
	ctx = context.WithValue(s.messageContext(ctx, client), managementAPIClientContextKey{}, client)
	clientID := client.id
	role := contextinfo.RoleUser
	if s.superadmins != nil && s.superadmins.IsSuperadmin(clientID) {
		role = contextinfo.RoleSuperadmin
	}
	actor := contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: s.Name(), ID: clientID}, Nickname: clientID, Name: clientID, Role: role}
	return contextinfo.WithActor(ctx, actor), nil
}

func (s *RemoteServer) scheduleControlContinuation(source context.Context, continuation control.Continuation) {
	s.mu.Lock()
	continueInput := s.continueInput
	if continueInput != nil && !s.closing {
		s.work.Add(1)
	} else {
		continueInput = nil
	}
	s.mu.Unlock()
	if continueInput != nil {
		go s.runControlContinuation(source, continuation, continueInput)
	}
}

func (s *RemoteServer) runControlContinuation(source context.Context, continuation control.Continuation, continueInput func(context.Context, control.Continuation) (context.Context, operationresult.Result[struct{}], error)) {
	defer s.work.Done()
	ctx, cancel := context.WithCancel(context.WithoutCancel(source))
	stopWork := context.AfterFunc(s.workCtx, cancel)
	defer func() { stopWork(); cancel() }()
	_, result, err := continueInput(ctx, continuation)
	if err == nil && result.Status == operationresult.Success {
		return
	}
	client, _ := source.Value(managementAPIClientContextKey{}).(*remoteClientConn)
	if client == nil {
		return
	}
	message := result.Message
	if err != nil {
		message = err.Error()
	}
	info, _ := contextinfo.ConversationFromContext(source)
	_ = client.write(s.transportCtx, remoteMessage{Type: remoteMsgError, SessionID: info.TargetSessionID, Text: message})
}

func writeManagementStopping(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "service_stopping", "message": "CLI 正在停止"}})
}
