package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/platform/cli/remote/management"
)

type ControlCapabilities = management.ControlCapabilities

type managementAPIClientContextKey struct{}

func (s *RemoteServer) managementHandler() http.Handler {
	handler := management.NewHandler(management.HandlerOptions{
		Control: s.control,
		Authenticate: func(ctx context.Context, req *http.Request) (context.Context, bool, error) {
			ctx, client, err := s.managementContext(ctx, req)
			if err != nil {
				return nil, false, err
			}
			if client != nil {
				ctx = context.WithValue(ctx, managementAPIClientContextKey{}, client)
			}
			return ctx, client != nil, nil
		},
		Continue: s.scheduleControlContinuation,
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

func (s *RemoteServer) managementContext(ctx context.Context, req *http.Request) (context.Context, *remoteClientConn, error) {
	clientID := strings.TrimSpace(req.Header.Get("X-Client-ID"))
	token, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok || clientID == "" || !s.validToken(clientID, token) {
		return nil, nil, errors.New("invalid client credentials")
	}
	var client *remoteClientConn
	if routeToken := req.Header.Get("X-CLI-Connection"); routeToken != "" {
		s.mu.Lock()
		client = s.apiClients[routeToken]
		s.mu.Unlock()
		if client == nil || client.id != clientID {
			return nil, nil, errors.New("invalid connection token")
		}
	}
	conversation := contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: s.Name(), ChatType: contextinfo.ChatPrivate, ChatID: clientID, Name: clientID},
		Identity: contextinfo.Identity{ID: clientID, Nickname: clientID, Name: clientID},
	}
	if client != nil {
		ctx = s.messageContext(ctx, client)
	} else {
		ctx = contextinfo.WithConversation(ctx, conversation)
	}
	role := contextinfo.RoleUser
	if s.superadmins != nil && s.superadmins.IsSuperadmin(clientID) {
		role = contextinfo.RoleSuperadmin
	}
	actor := contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: s.Name(), ID: clientID}, Nickname: clientID, Name: clientID, Role: role}
	return contextinfo.WithActor(ctx, actor), client, nil
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

func (s *RemoteServer) runControlContinuation(source context.Context, continuation control.Continuation, continueInput func(context.Context, control.Continuation) (context.Context, error)) {
	defer s.work.Done()
	ctx, cancel := context.WithCancel(context.WithoutCancel(source))
	stopWork := context.AfterFunc(s.workCtx, cancel)
	defer func() { stopWork(); cancel() }()
	_, err := continueInput(ctx, continuation)
	if err == nil {
		return
	}
	client, _ := source.Value(managementAPIClientContextKey{}).(*remoteClientConn)
	if client == nil {
		return
	}
	failure := control.ClassifyError(err)
	var classified *control.Error
	if errors.As(failure, &classified) {
		_ = client.write(s.transportCtx, remoteMessage{Type: remoteMsgError, Text: classified.Message})
	} else {
		_ = client.write(s.transportCtx, remoteMessage{Type: remoteMsgError, Text: "操作执行失败"})
	}
}

func writeManagementStopping(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "service_stopping", "message": "CLI 正在停止"}})
}
