package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/fileops"
	"elbot/internal/logging"
	"elbot/internal/tool"
)

// ControlCapabilities is the business surface exposed by the management API.
type ControlCapabilities interface {
	ListCommands(context.Context, control.ListCommandsRequest) (control.ListCommandsResult, error)
	Status(context.Context) (control.StatusResult, error)
	ListSessions(context.Context, control.ListSessionsRequest) (control.ListSessionsResult, error)
	GetSession(context.Context, control.GetSessionRequest) (control.GetSessionResult, error)
	NewSession(context.Context) (control.NewSessionResult, error)
	RenameSession(context.Context, control.RenameSessionRequest) (control.RenameSessionResult, error)
	SetSessionArchived(context.Context, control.SetSessionArchivedRequest) (control.SetSessionArchivedResult, error)
	SetSessionPinned(context.Context, control.SetSessionPinnedRequest) (control.SetSessionPinnedResult, error)
	DeleteSession(context.Context, control.DeleteSessionRequest) (control.DeleteSessionResult, error)
	CleanSessions(context.Context, control.CleanSessionsRequest) (control.CleanSessionsResult, error)
	ResumeSession(context.Context, control.ResumeSessionRequest) (control.ResumeSessionResult, error)
	ForkSession(context.Context, control.ForkSessionRequest) (control.ForkSessionResult, error)
	CompactSession(context.Context, control.CompactSessionRequest) (control.CompactSessionResult, error)
	SetMode(context.Context, control.SetModeRequest) (control.SetModeResult, error)
	SessionMessages(context.Context, control.SessionMessagesRequest) (control.SessionMessagesResult, error)
	ListModels(context.Context, control.ListModelsRequest) (control.ListModelsResult, error)
	RefreshModels(context.Context, control.ListModelsRequest) (control.RefreshModelsResult, error)
	SelectModel(context.Context, control.SelectModelRequest) (control.SelectModelResult, error)
	ListRequests(context.Context) (control.ListRequestsResult, error)
	StopRequest(context.Context, control.StopRequestRequest) (control.StopRequestResult, error)
	StopSession(context.Context) (control.StopSessionResult, error)
	StopAllRequests(context.Context) (control.StopAllRequestsResult, error)
	QueryLogs(context.Context, control.QueryLogsRequest) (control.QueryLogsResult, error)
	QueryUsage(context.Context, control.QueryUsageRequest) (control.QueryUsageResult, error)
	CheckConfiguration(context.Context) (control.CheckConfigurationResult, error)
	ListHooks(context.Context) (control.ListHooksResult, error)
	GetHook(context.Context, control.GetHookRequest) (control.GetHookResult, error)
	ReloadHooks(context.Context) (control.ReloadHooksResult, error)
	StartHook(context.Context, control.StartHookRequest) error
	StopHook(context.Context, control.StopHookRequest) error
	RestartHook(context.Context, control.RestartHookRequest) error
	CurrentConfirmation(context.Context, control.CurrentConfirmationRequest) (control.CurrentConfirmationResult, error)
	RespondConfirmation(context.Context, control.RespondConfirmationRequest) (control.RespondConfirmationResult, error)
	QueryTools(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error)
	ReloadSkills(context.Context) error
	RemoveSkill(context.Context, control.RemoveSkillRequest) error
	ListRollbacks(context.Context) ([]fileops.RollbackInfo, error)
	RollbackByID(context.Context, control.RollbackByIDRequest) (fileops.RollbackResult, error)
	ListFiles(context.Context, control.ListFilesRequest) (control.ListFilesResult, error)
	ReadFile(context.Context, control.ReadFileRequest) (control.ReadFileResult, error)
	WriteFile(context.Context, control.WriteFileRequest) (control.WriteFileResult, error)
	RequestRestart(context.Context) (control.RequestRestartResult, error)
}

func (s *RemoteServer) managementHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /commands", s.getCommands)
	mux.HandleFunc("GET /status", s.getStatus)
	mux.HandleFunc("GET /sessions", s.listSessions)
	mux.HandleFunc("POST /sessions", s.newSession)
	mux.HandleFunc("GET /sessions/{session_id}", s.getSession)
	mux.HandleFunc("GET /sessions/{session_id}/messages", s.sessionMessages)
	mux.HandleFunc("POST /sessions/{session_id}/resume", s.resumeSession)
	mux.HandleFunc("POST /sessions/{session_id}/fork", s.forkSession)
	mux.HandleFunc("PATCH /sessions/{session_id}", s.renameSession)
	mux.HandleFunc("POST /sessions/{session_id}/archive", s.archiveSession)
	mux.HandleFunc("POST /sessions/{session_id}/unarchive", s.unarchiveSession)
	mux.HandleFunc("POST /sessions/{session_id}/pin", s.pinSession)
	mux.HandleFunc("POST /sessions/{session_id}/unpin", s.unpinSession)
	mux.HandleFunc("DELETE /sessions/{session_id}", s.deleteSession)
	mux.HandleFunc("POST /sessions/clean", s.cleanSessions)
	mux.HandleFunc("POST /sessions/{session_id}/compact", s.compactSession)
	mux.HandleFunc("POST /session/mode", s.setMode)
	mux.HandleFunc("GET /models", s.listModels)
	mux.HandleFunc("POST /models/refresh", s.refreshModels)
	mux.HandleFunc("PUT /models/selection", s.selectModel)
	mux.HandleFunc("GET /requests", s.listRequests)
	mux.HandleFunc("POST /requests/{request_id}/stop", s.stopRequest)
	mux.HandleFunc("POST /requests/stop-session", s.stopSession)
	mux.HandleFunc("POST /requests/stop-all", s.stopAllRequests)
	mux.HandleFunc("GET /confirmation", s.currentConfirmation)
	mux.HandleFunc("POST /confirmation/respond", s.respondConfirmation)
	mux.HandleFunc("GET /tools", s.queryTools)
	mux.HandleFunc("POST /skills/reload", s.reloadSkills)
	mux.HandleFunc("DELETE /skills/{name}", s.removeSkill)
	mux.HandleFunc("GET /rollbacks", s.listRollbacks)
	mux.HandleFunc("POST /rollbacks/{id}/apply", s.rollbackByID)
	mux.HandleFunc("GET /hooks", s.listHooks)
	mux.HandleFunc("GET /hooks/{hook_id}", s.getHook)
	mux.HandleFunc("POST /hooks/reload", s.reloadHooks)
	mux.HandleFunc("POST /hooks/{hook_id}/start", s.startHook)
	mux.HandleFunc("POST /hooks/{hook_id}/stop", s.stopHook)
	mux.HandleFunc("POST /hooks/{hook_id}/restart", s.restartHook)
	mux.HandleFunc("GET /logs", s.queryLogs)
	mux.HandleFunc("GET /usage", s.queryUsage)
	mux.HandleFunc("GET /configuration/check", s.checkConfiguration)
	mux.HandleFunc("GET /files", s.listFiles)
	mux.HandleFunc("GET /files/content", s.readFile)
	mux.HandleFunc("PUT /files/content", s.writeFile)
	mux.HandleFunc("POST /runtime/restart", s.requestRestart)
	return http.StripPrefix("/api/v1", s.apiMiddleware(normalizeManagementErrors(mux)))
}

func normalizeManagementErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		errorWriter := &managementErrorWriter{ResponseWriter: w}
		var wrapped http.ResponseWriter = errorWriter
		if _, ok := w.(http.Flusher); ok {
			wrapped = managementFlushWriter{managementErrorWriter: errorWriter}
		}
		next.ServeHTTP(wrapped, req)
		if errorWriter.errorStatus == http.StatusNotFound {
			writeAPIError(w, http.StatusNotFound, "route_not_found", "接口不存在")
		} else if errorWriter.errorStatus == http.StatusMethodNotAllowed {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不支持")
		}
	})
}

type managementErrorWriter struct {
	http.ResponseWriter
	errorStatus int
	wroteHeader bool
}

func (w *managementErrorWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) && !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		w.errorStatus = status
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *managementErrorWriter) Write(body []byte) (int, error) {
	if w.errorStatus != 0 {
		return len(body), nil
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *managementErrorWriter) flush() {
	if w.errorStatus != 0 {
		return
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

type managementFlushWriter struct{ *managementErrorWriter }

func (w managementFlushWriter) Flush() { w.flush() }

func (w *managementErrorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (s *RemoteServer) apiMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			writeAPIError(w, http.StatusServiceUnavailable, "service_stopping", "CLI 正在停止")
			return
		}
		s.work.Add(1)
		s.mu.Unlock()
		defer s.work.Done()

		ctx, cancel := context.WithCancel(req.Context())
		stopWork := context.AfterFunc(s.workCtx, cancel)
		defer func() { stopWork(); cancel() }()
		ctx, client, err := s.managementContext(ctx, req)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "认证失败")
			return
		}
		if s.control == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "not_configured", "管理操作尚未接入")
			return
		}
		ctx = context.WithValue(ctx, apiClientContextKey{}, client)
		req = req.WithContext(ctx)
		next.ServeHTTP(w, req)
	})
}

func (s *RemoteServer) managementContext(ctx context.Context, req *http.Request) (context.Context, *remoteClientConn, error) {
	clientID := strings.TrimSpace(req.Header.Get("X-Client-ID"))
	authorization := req.Header.Get("Authorization")
	token, ok := strings.CutPrefix(authorization, "Bearer ")
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

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeAPIJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeAPIJSON(w http.ResponseWriter, status int, value any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(value)
}

func writeControlError(w http.ResponseWriter, err error) {
	var failure *control.Error
	if errors.As(err, &failure) {
		status := http.StatusInternalServerError
		switch failure.Code {
		case "invalid_arguments":
			status = http.StatusBadRequest
		case "permission_denied":
			status = http.StatusForbidden
		case "not_found", "no_current_session":
			status = http.StatusNotFound
		case "session_busy", "mode_conflict", "file_changed", "binding_changed", "confirmation_required", "confirmation_expired":
			status = http.StatusConflict
		case "timeout":
			status = http.StatusGatewayTimeout
		case "canceled":
			status = http.StatusRequestTimeout
		case "not_configured":
			status = http.StatusServiceUnavailable
		}
		writeAPIError(w, status, failure.Code, failure.Message)
		return
	}
	classified := control.ClassifyError(err)
	var classifiedFailure *control.Error
	if errors.As(classified, &classifiedFailure) {
		writeControlError(w, classifiedFailure)
		return
	}
	writeAPIError(w, http.StatusInternalServerError, "internal_error", "操作执行失败")
}

func decodeAPIJSON[T any](w http.ResponseWriter, req *http.Request) (T, bool) {
	var value T
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_arguments", "请求 JSON 无效")
		return value, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeAPIError(w, http.StatusBadRequest, "invalid_arguments", "请求只能包含一个 JSON 对象")
		return value, false
	}
	return value, true
}

func (s *RemoteServer) requireConnection(w http.ResponseWriter, req *http.Request) *remoteClientConn {
	client, _ := req.Context().Value(apiClientContextKey{}).(*remoteClientConn)
	if client == nil {
		writeAPIError(w, http.StatusConflict, "connection_required", "此操作需要当前 WebSocket 连接，请提供 X-CLI-Connection")
	}
	return client
}

type apiClientContextKey struct{}

func (s *RemoteServer) getCommands(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.ListCommands(req.Context(), control.ListCommandsRequest{Name: req.URL.Query().Get("name")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) getStatus(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.Status(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) listSessions(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	page, err := intQuery(q, "page", 1)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	pageSize, err := intQuery(q, "page_size", 20)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	archived, err := boolQuery(q, "archived")
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	exclude, err := boolQuery(q, "exclude_current")
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	result, err := s.control.ListSessions(req.Context(), control.ListSessionsRequest{Query: q.Get("query"), Page: page, PageSize: pageSize, Archived: archived, ExcludeCurrent: exclude})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) newSession(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.NewSession(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) getSession(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.GetSession(req.Context(), control.GetSessionRequest{SessionID: req.PathValue("session_id")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) sessionMessages(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	page, err := intQuery(q, "page", 1)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	pageSize, err := intQuery(q, "page_size", 20)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	result, err := s.control.SessionMessages(req.Context(), control.SessionMessagesRequest{SessionID: req.PathValue("session_id"), Page: page, PageSize: pageSize})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) resumeSession(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		MessageLimit int `json:"message_limit"`
	}](w, req)
	if !ok {
		return
	}
	result, err := s.control.ResumeSession(req.Context(), control.ResumeSessionRequest{SessionID: req.PathValue("session_id"), MessageLimit: body.MessageLimit})
	if err != nil {
		writeControlError(w, err)
		return
	}
	s.writeContinuationResult(w, req, result, result.Continuation)
}

func (s *RemoteServer) forkSession(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		MessageID    string `json:"message_id"`
		MessageLimit int    `json:"message_limit"`
	}](w, req)
	if !ok {
		return
	}
	result, err := s.control.ForkSession(req.Context(), control.ForkSessionRequest{SessionID: req.PathValue("session_id"), MessageID: body.MessageID, MessageLimit: body.MessageLimit})
	if err != nil {
		writeControlError(w, err)
		return
	}
	s.writeContinuationResult(w, req, result, result.Continuation)
}

func (s *RemoteServer) renameSession(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		Title string `json:"title"`
	}](w, req)
	if !ok {
		return
	}
	result, err := s.control.RenameSession(req.Context(), control.RenameSessionRequest{Target: req.PathValue("session_id"), Title: body.Title})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) archiveSession(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		Confirmed bool `json:"confirmed"`
	}](w, req)
	if !ok {
		return
	}
	if !body.Confirmed {
		writeAPIError(w, http.StatusBadRequest, "confirmation_required", "归档会话需要确认")
		return
	}
	result, err := s.control.SetSessionArchived(req.Context(), control.SetSessionArchivedRequest{SessionID: req.PathValue("session_id"), Archived: true, Confirmed: body.Confirmed})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) unarchiveSession(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.SetSessionArchived(req.Context(), control.SetSessionArchivedRequest{SessionID: req.PathValue("session_id"), Archived: false})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) pinSession(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.SetSessionPinned(req.Context(), control.SetSessionPinnedRequest{SessionID: req.PathValue("session_id"), Pinned: true})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) unpinSession(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.SetSessionPinned(req.Context(), control.SetSessionPinnedRequest{SessionID: req.PathValue("session_id"), Pinned: false})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) deleteSession(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		Confirmed bool `json:"confirmed"`
	}](w, req)
	if !ok {
		return
	}
	if !body.Confirmed {
		writeAPIError(w, http.StatusBadRequest, "confirmation_required", "删除会话需要确认")
		return
	}
	result, err := s.control.DeleteSession(req.Context(), control.DeleteSessionRequest{SessionID: req.PathValue("session_id"), Confirmed: body.Confirmed})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) cleanSessions(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[control.CleanSessionsRequest](w, req)
	if !ok {
		return
	}
	result, err := s.control.CleanSessions(req.Context(), body)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) compactSession(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.CompactSession(req.Context(), control.CompactSessionRequest{SessionID: req.PathValue("session_id")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) setMode(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		Mode            string `json:"mode"`
		NewSessionTitle string `json:"new_session_title"`
	}](w, req)
	if !ok {
		return
	}
	if body.NewSessionTitle == "" {
		switch body.Mode {
		case "chat":
			body.NewSessionTitle = "New chat session"
		case "work":
			body.NewSessionTitle = "New work session"
		}
	}
	result, err := s.control.SetMode(req.Context(), control.SetModeRequest{Mode: body.Mode, NewSessionTitle: body.NewSessionTitle})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) listModels(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.ListModels(req.Context(), control.ListModelsRequest{Query: req.URL.Query().Get("query")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) refreshModels(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.RefreshModels(req.Context(), control.ListModelsRequest{Query: req.URL.Query().Get("query")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) selectModel(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[control.SelectModelRequest](w, req)
	if !ok {
		return
	}
	result, err := s.control.SelectModel(req.Context(), body)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) listRequests(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.ListRequests(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) stopRequest(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.StopRequest(req.Context(), control.StopRequestRequest{RequestID: req.PathValue("request_id")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) stopSession(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.StopSession(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) stopAllRequests(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.StopAllRequests(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) currentConfirmation(w http.ResponseWriter, req *http.Request) {
	refresh, err := boolQuery(req.URL.Query(), "refresh")
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	result, err := s.control.CurrentConfirmation(req.Context(), control.CurrentConfirmationRequest{Refresh: refresh})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) respondConfirmation(w http.ResponseWriter, req *http.Request) {
	if s.requireConnection(w, req) == nil {
		return
	}
	body, ok := decodeAPIJSON[control.RespondConfirmationRequest](w, req)
	if !ok {
		return
	}
	result, err := s.control.RespondConfirmation(req.Context(), body)
	if err != nil {
		writeControlError(w, err)
		return
	}
	s.writeContinuationResult(w, req, result, result.Continuation)
}

func (s *RemoteServer) queryTools(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	scope := tool.QueryAvailable
	if q.Get("scope") == "catalog" {
		scope = tool.QueryCatalog
	}
	if q.Get("scope") != "" && q.Get("scope") != "catalog" && q.Get("scope") != "available" {
		writeAPIError(w, http.StatusBadRequest, "invalid_arguments", "scope 必须是 available 或 catalog")
		return
	}
	hidden, err := boolQuery(q, "include_hidden")
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	discover, err := boolQuery(q, "include_discover")
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	items, err := s.control.QueryTools(req.Context(), tool.ToolQuery{Scope: scope, Tag: q.Get("tag"), Kind: q.Get("kind"), Names: q["name"], IncludeHidden: hidden, IncludeDiscover: discover})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *RemoteServer) reloadSkills(w http.ResponseWriter, req *http.Request) {
	if err := s.control.ReloadSkills(req.Context()); err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *RemoteServer) removeSkill(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[struct {
		Confirmed bool `json:"confirmed"`
	}](w, req)
	if !ok {
		return
	}
	if err := s.control.RemoveSkill(req.Context(), control.RemoveSkillRequest{Name: req.PathValue("name"), Confirmed: body.Confirmed}); err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *RemoteServer) listRollbacks(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.ListRollbacks(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (s *RemoteServer) rollbackByID(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.ParseUint(req.PathValue("id"), 10, 64)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_arguments", "撤销编号无效")
		return
	}
	result, err := s.control.RollbackByID(req.Context(), control.RollbackByIDRequest{ID: id})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) listHooks(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.ListHooks(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) getHook(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.GetHook(req.Context(), control.GetHookRequest{HookID: req.PathValue("hook_id")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) reloadHooks(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.ReloadHooks(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) startHook(w http.ResponseWriter, req *http.Request) {
	s.hookAction(w, req, s.control.StartHook)
}
func (s *RemoteServer) stopHook(w http.ResponseWriter, req *http.Request) {
	s.hookAction(w, req, s.control.StopHook)
}
func (s *RemoteServer) restartHook(w http.ResponseWriter, req *http.Request) {
	s.hookAction(w, req, s.control.RestartHook)
}

func (s *RemoteServer) hookAction(w http.ResponseWriter, req *http.Request, action func(context.Context, control.GetHookRequest) error) {
	if err := action(req.Context(), control.GetHookRequest{HookID: req.PathValue("hook_id")}); err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *RemoteServer) queryLogs(w http.ResponseWriter, req *http.Request) {
	q, err := logQuery(req.URL.Query())
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	result, err := s.control.QueryLogs(req.Context(), control.QueryLogsRequest{Category: req.URL.Query().Get("category"), Query: q})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) queryUsage(w http.ResponseWriter, req *http.Request) {
	q, err := logQuery(req.URL.Query())
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	first, err := intQuery(req.URL.Query(), "first", 1)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	last, err := intQuery(req.URL.Query(), "last", 100000)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	group := req.URL.Query().Get("group_by")
	if group == "" {
		group = "model"
	}
	result, err := s.control.QueryUsage(req.Context(), control.QueryUsageRequest{Query: q, GroupBy: group, First: first, Last: last})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) checkConfiguration(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.CheckConfiguration(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) listFiles(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.ListFiles(req.Context(), control.ListFilesRequest{Root: req.URL.Query().Get("root"), Path: req.URL.Query().Get("path")})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) readFile(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	offset, err := int64Query(q, "offset", 0)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	limit, err := int64Query(q, "limit", 0)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	result, err := s.control.ReadFile(req.Context(), control.ReadFileRequest{Root: q.Get("root"), Path: q.Get("path"), Offset: offset, Limit: limit})
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) writeFile(w http.ResponseWriter, req *http.Request) {
	body, ok := decodeAPIJSON[control.WriteFileRequest](w, req)
	if !ok {
		return
	}
	result, err := s.control.WriteFile(req.Context(), body)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *RemoteServer) requestRestart(w http.ResponseWriter, req *http.Request) {
	result, err := s.control.RequestRestart(req.Context())
	if err != nil {
		writeControlError(w, err)
		return
	}
	if err := writeAPIJSON(w, http.StatusAccepted, map[string]bool{"accepted": result.Accepted}); err != nil {
		if result.Cancel != nil {
			result.Cancel()
		}
		return
	}
	if result.Commit != nil {
		if err := result.Commit(context.WithoutCancel(req.Context())); err != nil {
			// The response has already been delivered; the runtime owns reporting a failed commit.
		}
	}
}

func (s *RemoteServer) writeContinuationResult(w http.ResponseWriter, req *http.Request, result any, continuation *control.Continuation) {
	if err := writeAPIJSON(w, http.StatusOK, result); err != nil || continuation == nil {
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	s.mu.Lock()
	continueInput := s.continueInput
	if continueInput != nil && !s.closing {
		s.work.Add(1)
	} else {
		continueInput = nil
	}
	s.mu.Unlock()
	if continueInput != nil {
		go s.runControlContinuation(req.Context(), *continuation, continueInput)
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
	if client := s.apiClientFromContext(source); client != nil {
		failure := control.ClassifyError(err)
		var classified *control.Error
		if errors.As(failure, &classified) {
			_ = client.write(s.transportCtx, remoteMessage{Type: remoteMsgError, Text: classified.Message})
		} else {
			_ = client.write(s.transportCtx, remoteMessage{Type: remoteMsgError, Text: "操作执行失败"})
		}
	}
}

func (s *RemoteServer) apiClientFromContext(ctx context.Context) *remoteClientConn {
	client, _ := ctx.Value(apiClientContextKey{}).(*remoteClientConn)
	return client
}

func writeBadQuery(w http.ResponseWriter, err error) {
	writeAPIError(w, http.StatusBadRequest, "invalid_arguments", err.Error())
}

func intQuery(q url.Values, key string, fallback int) (int, error) {
	value := q.Get(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return parsed, nil
}

func int64Query(q url.Values, key string, fallback int64) (int64, error) {
	value := q.Get(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return parsed, nil
}

func boolQuery(q url.Values, key string) (bool, error) {
	value := q.Get(key)
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func logQuery(q url.Values) (logging.LogQuery, error) {
	limit, err := intQuery(q, "limit", logging.DefaultLogLimit)
	if err != nil {
		return logging.LogQuery{}, err
	}
	days, err := intQuery(q, "days", 1)
	if err != nil {
		return logging.LogQuery{}, err
	}
	raw, err := boolQuery(q, "raw")
	if err != nil {
		return logging.LogQuery{}, err
	}
	query := logging.LogQuery{Limit: limit, Days: days, MinLevel: q.Get("level"), Events: q["event"], Contains: q.Get("contains"), ContentContains: q.Get("content_contains"), Raw: raw, Fields: map[string]string{}, ContentFields: map[string]string{}, ContentFieldContains: map[string][]string{}}
	if value := q.Get("since"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return query, fmt.Errorf("since must be RFC3339")
		}
		query.Since = &parsed
	}
	if value := q.Get("until"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return query, fmt.Errorf("until must be RFC3339")
		}
		query.Until = &parsed
	}
	if err := addQueryPairs(query.Fields, q["field"]); err != nil {
		return query, err
	}
	if err := addQueryPairs(query.ContentFields, q["content_field"]); err != nil {
		return query, err
	}
	for _, pair := range q["content_field_contains"] {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return query, errors.New("content_field_contains must use key=value")
		}
		query.ContentFieldContains[key] = append(query.ContentFieldContains[key], value)
	}
	return query, nil
}

func addQueryPairs(target map[string]string, pairs []string) error {
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return errors.New("field filters must use key=value")
		}
		target[key] = value
	}
	return nil
}
