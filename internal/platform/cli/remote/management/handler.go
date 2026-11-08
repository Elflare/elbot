package management

import (
	"context"
	"net/http"

	"elbot/internal/control"
)

// HandlerOptions supplies the business and transport boundaries used by the API.
type HandlerOptions struct {
	// Control exposes the business operations served by the HTTP API.
	Control ControlCapabilities
	// Authenticate validates the request and returns its actor context and connection binding.
	Authenticate func(context.Context, *http.Request) (context.Context, bool, error)
	// Continue schedules a continuation after its HTTP result has been written.
	Continue func(context.Context, control.Continuation)
}

type handler struct {
	control      ControlCapabilities
	authenticate func(context.Context, *http.Request) (context.Context, bool, error)
	continueCall func(context.Context, control.Continuation)
}

// NewHandler builds the management API under the /api/v1 prefix.
func NewHandler(options HandlerOptions) http.Handler {
	h := &handler{control: options.Control, authenticate: options.Authenticate, continueCall: options.Continue}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /commands", h.getCommands)
	mux.HandleFunc("GET /status", h.getStatus)
	mux.HandleFunc("GET /sessions", h.listSessions)
	mux.HandleFunc("POST /sessions", h.newSession)
	mux.HandleFunc("GET /sessions/{session_id}", h.getSession)
	mux.HandleFunc("GET /sessions/{session_id}/messages", h.sessionMessages)
	mux.HandleFunc("POST /sessions/{session_id}/resume", h.resumeSession)
	mux.HandleFunc("POST /sessions/{session_id}/fork", h.forkSession)
	mux.HandleFunc("PATCH /sessions/{session_id}", h.renameSession)
	mux.HandleFunc("POST /sessions/{session_id}/archive", h.archiveSession)
	mux.HandleFunc("POST /sessions/{session_id}/unarchive", h.unarchiveSession)
	mux.HandleFunc("POST /sessions/{session_id}/pin", h.pinSession)
	mux.HandleFunc("POST /sessions/{session_id}/unpin", h.unpinSession)
	mux.HandleFunc("DELETE /sessions/{session_id}", h.deleteSession)
	mux.HandleFunc("POST /sessions/clean", h.cleanSessions)
	mux.HandleFunc("POST /sessions/{session_id}/compact", h.compactSession)
	mux.HandleFunc("POST /session/mode", h.setMode)
	mux.HandleFunc("GET /models", h.listModels)
	mux.HandleFunc("POST /models/refresh", h.refreshModels)
	mux.HandleFunc("PUT /models/selection", h.selectModel)
	mux.HandleFunc("GET /requests", h.listRequests)
	mux.HandleFunc("POST /requests/{request_id}/stop", h.stopRequest)
	mux.HandleFunc("POST /requests/stop-session", h.stopSession)
	mux.HandleFunc("POST /requests/stop-all", h.stopAllRequests)
	mux.HandleFunc("GET /confirmation", h.currentConfirmation)
	mux.HandleFunc("POST /confirmation/respond", h.respondConfirmation)
	mux.HandleFunc("GET /tools", h.queryTools)
	mux.HandleFunc("POST /skills/reload", h.reloadSkills)
	mux.HandleFunc("DELETE /skills/{name}", h.removeSkill)
	mux.HandleFunc("GET /rollbacks", h.listRollbacks)
	mux.HandleFunc("POST /rollbacks/{id}/apply", h.rollbackByID)
	mux.HandleFunc("GET /hooks", h.listHooks)
	mux.HandleFunc("GET /hooks/{hook_id}", h.getHook)
	mux.HandleFunc("POST /hooks/reload", h.reloadHooks)
	mux.HandleFunc("POST /hooks/{hook_id}/start", h.startHook)
	mux.HandleFunc("POST /hooks/{hook_id}/stop", h.stopHook)
	mux.HandleFunc("POST /hooks/{hook_id}/restart", h.restartHook)
	mux.HandleFunc("GET /logs", h.queryLogs)
	mux.HandleFunc("GET /usage", h.queryUsage)
	mux.HandleFunc("GET /configuration/check", h.checkConfiguration)
	mux.HandleFunc("GET /files", h.listFiles)
	mux.HandleFunc("GET /files/content", h.readFile)
	mux.HandleFunc("PUT /files/content", h.writeFile)
	mux.HandleFunc("POST /runtime/restart", h.requestRestart)
	return http.StripPrefix("/api/v1", normalizeManagementErrors(h.apiMiddleware(mux)))
}

func (h *handler) writeContinuationResult(w http.ResponseWriter, req *http.Request, result any, continuation *control.Continuation) {
	if err := writeAPIJSON(w, http.StatusOK, result); err != nil || continuation == nil || h.continueCall == nil {
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	h.continueCall(req.Context(), *continuation)
}
